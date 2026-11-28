package openshell

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "github.com/SAP/astonish/pkg/sandbox/openshell/gen/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type inventoryWireServer struct {
	pb.UnimplementedOpenShellServer
	list func(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error)
}

func (s *inventoryWireServer) ListSandboxes(ctx context.Context, req *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	return s.list(ctx, req)
}

func inventoryWireClient(t *testing.T, list func(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error)) *grpcGatewayClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterOpenShellServer(server, &inventoryWireServer{list: list})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///inventory", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &grpcGatewayClient{conn: conn, client: pb.NewOpenShellClient(conn)}
}

func inventorySandbox(i int) *pb.Sandbox {
	return &pb.Sandbox{
		Metadata: &pb.ObjectMeta{Id: fmt.Sprintf("uuid-%d", i), Name: fmt.Sprintf("sandbox-%d", i), Labels: map[string]string{"owner": "bot"}, CreatedAtMs: 123456},
		Status:   &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_READY},
	}
}

func TestGRPCInventoryPagination(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count, cap int
		offsets    []uint32
	}{
		{"beyond500", 501, 500, []uint32{0, 500, 501}},
		{"exact500", 500, 500, []uint32{0, 500}},
		{"empty", 0, 500, []uint32{0}},
		{"serverClamp", 5, 2, []uint32{0, 2, 4, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var offsets []uint32
			client := inventoryWireClient(t, func(_ context.Context, req *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
				if req.GetLimit() != 500 || req.GetLabelSelector() != "owner=bot" {
					return nil, status.Error(codes.InvalidArgument, "limit/selector changed")
				}
				offsets = append(offsets, req.GetOffset())
				resp := &pb.ListSandboxesResponse{}
				for i := int(req.GetOffset()); i < tc.count && i < int(req.GetOffset())+tc.cap; i++ {
					resp.Sandboxes = append(resp.Sandboxes, inventorySandbox(i))
				}
				return resp, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := client.ListSandboxes(ctx, "owner=bot")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.count || !reflect.DeepEqual(offsets, tc.offsets) {
				t.Fatalf("count=%d offsets=%v", len(got), offsets)
			}
			for i, s := range got {
				if s.ID != fmt.Sprintf("uuid-%d", i) || s.Name != fmt.Sprintf("sandbox-%d", i) || s.Labels["owner"] != "bot" || s.CreatedAt.UnixMilli() != 123456 || s.Phase != pb.SandboxPhase_SANDBOX_PHASE_READY.String() {
					t.Fatalf("incorrect summary: %+v", s)
				}
			}
		})
	}
}

func TestGRPCInventoryRejectsIncomplete(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"repeatedPage", "repeated sandbox ID"},
		{"repeatedName", "repeated sandbox name"},
		{"secondPageFailure", "Unavailable"},
		{"missingID", "missing sandbox identity"},
		{"missingName", "missing sandbox identity"},
		{"missingMetadata", "missing sandbox identity"},
		{"oversized", "exceeds requested limit"},
		{"budget", "page budget exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := inventoryWireClient(t, func(_ context.Context, req *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
				calls++
				if req.GetLabelSelector() != "" || req.GetOffset() != uint32(calls-1) {
					return nil, status.Error(codes.InvalidArgument, "wrong cursor")
				}
				s := inventorySandbox(calls - 1)
				if calls > 1 {
					switch tc.name {
					case "repeatedPage":
						s = inventorySandbox(0)
					case "repeatedName":
						s.Metadata.Name = "sandbox-0"
					case "secondPageFailure":
						return nil, status.Error(codes.Unavailable, "page failed")
					case "missingID":
						s.Metadata.Id = ""
					case "missingName":
						s.Metadata.Name = ""
					case "missingMetadata":
						s.Metadata = nil
					case "oversized":
						resp := &pb.ListSandboxesResponse{}
						for i := 0; i < 501; i++ {
							resp.Sandboxes = append(resp.Sandboxes, inventorySandbox(i+1))
						}
						return resp, nil
					}
				}
				return &pb.ListSandboxesResponse{Sandboxes: []*pb.Sandbox{s}}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := client.ListSandboxes(ctx, "")
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("partial inventory=%v error=%v", got, err)
			}
			wantCalls := 2
			if tc.name == "budget" {
				wantCalls = gatewayInventoryMaxPages
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestGRPCInventoryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := inventoryWireClient(t, func(_ context.Context, req *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
		if req.GetOffset() > 0 {
			cancel()
			return nil, status.Error(codes.Canceled, "canceled")
		}
		return &pb.ListSandboxesResponse{Sandboxes: []*pb.Sandbox{inventorySandbox(0)}}, nil
	})
	got, err := client.ListSandboxes(ctx, "")
	if got != nil || err == nil {
		t.Fatalf("partial inventory=%v error=%v", got, err)
	}
}

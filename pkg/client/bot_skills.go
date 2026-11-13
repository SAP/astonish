package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/SAP/astonish/pkg/store"
)

func (b Bots) skillPath(id, suffix string, page store.BotSkillPage) (string, error) {
	if err := page.Validate(); err != nil {
		return "", err
	}
	path := b.path("/" + url.PathEscape(id) + "/skills" + suffix)
	if page.Limit > 0 {
		path += "&limit=" + strconv.Itoa(page.Limit)
	}
	if page.After != "" {
		path += "&after=" + url.QueryEscape(page.After)
	}
	if page.IncludeArchived {
		path += "&include_archived=true"
	}
	return path, nil
}
func (b Bots) Skills(ctx context.Context, id string, page store.BotSkillPage) (*store.BotSkillList, error) {
	path, err := b.skillPath(id, "", page)
	if err != nil {
		return nil, err
	}
	var out store.BotSkillList
	err = b.request(ctx, "GET", path, nil, &out)
	return &out, err
}
func (b Bots) Skill(ctx context.Context, id, skill string) (*store.BotSkill, error) {
	var out store.BotSkill
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/skills/"+url.PathEscape(skill)), nil, &out)
	return &out, err
}
func (b Bots) SkillRevision(ctx context.Context, id, skill string, revision int64) (*store.BotSkillRevision, error) {
	suffix := "/head"
	if revision > 0 {
		suffix = "/revisions/" + strconv.FormatInt(revision, 10)
	}
	var out store.BotSkillRevision
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/skills/"+url.PathEscape(skill)+suffix), nil, &out)
	return &out, err
}
func (b Bots) SkillRevisions(ctx context.Context, id, skill string, page store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	path, err := b.skillPath(id, "/"+url.PathEscape(skill)+"/revisions", page)
	if err != nil {
		return nil, err
	}
	var out store.BotSkillRevisionList
	err = b.request(ctx, "GET", path, nil, &out)
	return &out, err
}
func (b Bots) SkillRuns(ctx context.Context, id, skill string, page store.BotSkillPage) (*store.BotSkillRunList, error) {
	path, err := b.skillPath(id, "/"+url.PathEscape(skill)+"/runs", page)
	if err != nil {
		return nil, err
	}
	var out store.BotSkillRunList
	err = b.request(ctx, "GET", path, nil, &out)
	return &out, err
}

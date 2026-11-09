package main

import "errors"

// redactedAuthentication prevents Rod evaluation errors (which may contain
// login arguments) from escaping into failure artifacts or console output.
func redactedAuthentication(login func()) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("browser authentication failed")
		}
	}()
	login()
	return nil
}

// authenticateBrowser reuses the login capability owned by main. Credentials
// never enter checkpoints, receipts, or the restart subprocess environment.
type authenticateBrowser func() error

func restartAndAuthenticate(cp *smokeCheckpoint, restart func() error, persist func(), authenticate authenticateBrowser) error {
	if !cp.Restarted {
		if err := restart(); err != nil {
			return err
		}
		cp.Restarted = true
		persist()
	}
	// Restart invalidates the daemon's authentication session. Reauthenticate in
	// this invocation before reloading Home or reading its protected history.
	return authenticate()
}

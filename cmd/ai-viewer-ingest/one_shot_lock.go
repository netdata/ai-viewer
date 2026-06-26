package main

import (
	"log/slog"
	"os"
)

func acquireOneShotDaemonLock(stateDirFlag string, logger *slog.Logger) (func(), string, error) {
	stateDir, err := resolveStateDir(stateDirFlag)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(stateDir, stateDirPerm); err != nil {
		return nil, stateDir, err
	}
	release, err := acquireSingleInstanceLock(stateDir, logger)
	if err != nil {
		return nil, stateDir, err
	}
	return release, stateDir, nil
}

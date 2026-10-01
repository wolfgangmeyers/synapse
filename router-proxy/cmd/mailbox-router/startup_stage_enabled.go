//go:build embedded_private_codex_observer

package main

import (
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// Fixed diagnostic vocabulary. An absent result (or a partial file) is UNKNOWN.
type startupStage string

const (
	stageCLI           startupStage = "cli"
	stageCredential    startupStage = "credential"
	stageConfig        startupStage = "config"
	stageStoreManifest startupStage = "store_manifest"
	stageHelper        startupStage = "helper"
	stageListen        startupStage = "listen"
	stageRootAdmission startupStage = "root_admission"
)

var errStartupObserver = errors.New("private startup observer write failed")

func startupEntered(stage startupStage) error {
	if managed.WritePrivateStartupStage(string(stage), "entered", "entered") != nil {
		return errStartupObserver
	}
	return nil
}

func startupResult(stage startupStage, result error) error {
	category := "success"
	if result != nil {
		category = "failed"
	}
	if managed.WritePrivateStartupStage(string(stage), "result", category) != nil {
		return errStartupObserver
	}
	return result
}

func startupSnapshotUnavailable() error {
	if managed.WritePrivateStartupStage(string(stageHelper), "result", "snapshot_unavailable_continued") != nil {
		return errStartupObserver
	}
	return nil
}

func startupRun(stage startupStage, operation func() error) error {
	if err := startupEntered(stage); err != nil {
		return err
	}
	return startupResult(stage, operation())
}

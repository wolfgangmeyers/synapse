//go:build !embedded_private_codex_observer

package main

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

func startupEntered(startupStage) error                       { return nil }
func startupResult(_ startupStage, result error) error        { return result }
func startupSnapshotUnavailable() error                       { return nil }
func startupRun(_ startupStage, operation func() error) error { return operation() }

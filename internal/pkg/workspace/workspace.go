// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package workspace contains functionality to manage a user's local workspace. This includes
// creating an application directory, reading and writing a summary file to associate the workspace with the application,
// and managing infrastructure-as-code files. The typical workspace will be structured like:
//  .
//  ├── copilot                        (application directory)
//  │   ├── .workspace                 (workspace summary)
//  │   └── my-service
//  │   │   └── manifest.yml           (service manifest)
//  │   ├── buildspec.yml              (buildspec for the pipeline's build stage)
//  │   └── pipeline.yml               (pipeline manifest)
//  └── my-service-src                 (customer service code)
package workspace

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/aws/copilot-cli/internal/pkg/manifest"
	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

const (
	// CopilotDirName is the name of the directory where generated infrastructure code for an application will be stored.
	CopilotDirName = "copilot"
	// SummaryFileName is the name of the file that is associated with the application.
	SummaryFileName = ".workspace"

	addonsDirName             = "addons"
	maximumParentDirsToSearch = 5
	pipelineFileName          = "pipeline.yml"
	manifestFileName          = "manifest.yml"
	buildspecFileName         = "buildspec.yml"

	ymlFileExtension = ".yml"

	dockerfileName = "Dockerfile"
)

// Summary is a description of what's associated with this workspace.
type Summary struct {
	Application string `yaml:"application"` // Name of the application.
}

// Workspace typically represents a Git repository where the user has its infrastructure-as-code files as well as source files.
type Workspace struct {
	workingDir string
	copilotDir string
	fsUtils    *afero.Afero
}

// New returns a workspace, used for reading and writing to user's local workspace.
func New() (*Workspace, error) {
	fs := afero.NewOsFs()
	fsUtils := &afero.Afero{Fs: fs}

	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws := Workspace{
		workingDir: workingDir,
		fsUtils:    fsUtils,
	}

	return &ws, nil
}

// Create creates the copilot directory (if it doesn't already exist) in the current working directory,
// and saves a summary with the application name.
func (ws *Workspace) Create(appName string) error {
	// Create an application directory, if one doesn't exist
	if err := ws.createCopilotDir(); err != nil {
		return err
	}

	// Grab an existing workspace summary, if one exists.
	summary, err := ws.Summary()
	if err == nil {
		// If a summary exists, but is registered to a different application, throw an error.
		if summary.Application != appName {
			return &errHasExistingApplication{existingAppName: summary.Application}
		}
		// Otherwise our work is all done.
		return nil
	}

	// If there isn't an existing workspace summary, create it.
	var notFound *errNoAssociatedApplication
	if errors.As(err, &notFound) {
		return ws.writeSummary(appName)
	}

	return err
}

// Summary returns a summary of the workspace - including the application name.
func (ws *Workspace) Summary() (*Summary, error) {
	summaryPath, err := ws.summaryPath()
	if err != nil {
		return nil, err
	}
	summaryFileExists, _ := ws.fsUtils.Exists(summaryPath) // If an err occurs, return no applications.
	if summaryFileExists {
		value, err := ws.fsUtils.ReadFile(summaryPath)
		if err != nil {
			return nil, err
		}
		wsSummary := Summary{}
		return &wsSummary, yaml.Unmarshal(value, &wsSummary)
	}
	return nil, &errNoAssociatedApplication{}
}

// ServiceNames returns the names of the services in the workspace.
func (ws *Workspace) ServiceNames() ([]string, error) {
	return ws.workloadNames(func(wlType string) bool {
		for _, t := range manifest.ServiceTypes {
			if wlType == t {
				return true
			}
		}
		return false
	})
}

// JobNames returns the names of all jobs in the workspace.
func (ws *Workspace) JobNames() ([]string, error) {
	return ws.workloadNames(func(wlType string) bool {
		for _, t := range manifest.JobTypes {
			if wlType == t {
				return true
			}
		}
		return false
	})
}

// workloadNames returns the name of all workloads (either services or jobs) in the workspace.
func (ws *Workspace) workloadNames(match func(string) bool) ([]string, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return nil, err
	}
	files, err := ws.fsUtils.ReadDir(copilotPath)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", copilotPath, err)
	}
	var names []string
	for _, f := range files {
		if !f.IsDir() {
			continue
		}
		if exists, _ := ws.fsUtils.Exists(filepath.Join(copilotPath, f.Name(), manifestFileName)); !exists {
			// Swallow the error because we don't want to include any services that we don't have permissions to read.
			continue
		}
		manifestBytes, err := ws.readWorkloadManifest(f.Name())
		if err != nil {
			return nil, fmt.Errorf("read manifest for workload %s: %w", f.Name(), err)
		}
		wlType, err := ws.readWorkloadType(manifestBytes)
		if err != nil {
			return nil, err
		}
		if match(wlType) {
			names = append(names, f.Name())
		}
	}
	return names, nil
}

// ReadServiceManifest returns the contents of the service's manifest under copilot/{name}/manifest.yml.
func (ws *Workspace) ReadServiceManifest(name string) ([]byte, error) {
	mf, err := ws.readWorkloadManifest(name)
	if err != nil {
		return nil, fmt.Errorf("read service %s manifest file: %w", name, err)
	}
	return mf, nil
}

// ReadJobManifest returns the contents of the job's manifest under copilot/{name}/manifest.yml.
func (ws *Workspace) ReadJobManifest(name string) ([]byte, error) {
	mf, err := ws.readWorkloadManifest(name)
	if err != nil {
		return nil, fmt.Errorf("read job %s manifest file: %w", name, err)
	}
	return mf, nil
}

func (ws *Workspace) readWorkloadManifest(name string) ([]byte, error) {
	return ws.read(name, manifestFileName)
}

// ReadPipelineManifest returns the contents of the pipeline manifest under copilot/pipeline.yml.
func (ws *Workspace) ReadPipelineManifest() ([]byte, error) {
	pmPath, err := ws.pipelineManifestPath()
	if err != nil {
		return nil, err
	}
	manifestExists, err := ws.fsUtils.Exists(pmPath)

	if err != nil {
		return nil, err
	}
	if !manifestExists {
		return nil, ErrNoPipelineInWorkspace
	}
	return ws.read(pipelineFileName)
}

// WriteServiceManifest writes the service's manifest under the copilot/{name}/ directory.
func (ws *Workspace) WriteServiceManifest(marshaler encoding.BinaryMarshaler, name string) (string, error) {
	data, err := marshaler.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal service %s manifest to binary: %w", name, err)
	}
	return ws.write(data, name, manifestFileName)
}

// WriteJobManifest writes the job's manifest under the copilot/{name}/ directory.
func (ws *Workspace) WriteJobManifest(marshaler encoding.BinaryMarshaler, name string) (string, error) {
	data, err := marshaler.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal job %s manifest to binary: %w", name, err)
	}
	return ws.write(data, name, manifestFileName)
}

// WritePipelineBuildspec writes the pipeline buildspec under the copilot/ directory.
// If successful returns the full path of the file, otherwise returns an empty string and the error.
func (ws *Workspace) WritePipelineBuildspec(marshaler encoding.BinaryMarshaler) (string, error) {
	data, err := marshaler.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal pipeline buildspec to binary: %w", err)
	}
	return ws.write(data, buildspecFileName)
}

// WritePipelineManifest writes the pipeline manifest under the copilot directory.
// If successful returns the full path of the file, otherwise returns an empty string and the error.
func (ws *Workspace) WritePipelineManifest(marshaler encoding.BinaryMarshaler) (string, error) {
	data, err := marshaler.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal pipeline manifest to binary: %w", err)
	}
	return ws.write(data, pipelineFileName)
}

// DeleteWorkspaceFile removes the .workspace file under copilot/ directory.
// This will be called during app delete, we do not want to delete any other generated files.
func (ws *Workspace) DeleteWorkspaceFile() error {
	return ws.fsUtils.Remove(filepath.Join(CopilotDirName, SummaryFileName))
}

// ReadAddonsDir returns a list of file names under a service's "addons/" directory.
func (ws *Workspace) ReadAddonsDir(svcName string) ([]string, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return nil, err
	}

	var names []string
	files, err := ws.fsUtils.ReadDir(filepath.Join(copilotPath, svcName, addonsDirName))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		names = append(names, f.Name())
	}
	return names, nil
}

// ReadAddon returns the contents of a file under the service's "addons/" directory.
func (ws *Workspace) ReadAddon(svc, fname string) ([]byte, error) {
	return ws.read(svc, addonsDirName, fname)
}

// WriteAddon writes the content of an addon file under "{svc}/addons/{name}.yml".
// If successful returns the full path of the file, otherwise an empty string and an error.
func (ws *Workspace) WriteAddon(content encoding.BinaryMarshaler, svc, name string) (string, error) {
	data, err := content.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal binary addon content: %w", err)
	}
	fname := name + ymlFileExtension
	return ws.write(data, svc, addonsDirName, fname)
}

// FileStat wraps the os.Stat function.
type FileStat interface {
	Stat(name string) (os.FileInfo, error)
}

// IsInGitRepository returns true if the current working directory is a git repository.
func IsInGitRepository(fs FileStat) bool {
	_, err := fs.Stat(".git")
	return !os.IsNotExist(err)
}

func (ws *Workspace) writeSummary(appName string) error {
	summaryPath, err := ws.summaryPath()
	if err != nil {
		return err
	}

	workspaceSummary := Summary{
		Application: appName,
	}

	serializedWorkspaceSummary, err := yaml.Marshal(workspaceSummary)

	if err != nil {
		return err
	}
	return ws.fsUtils.WriteFile(summaryPath, serializedWorkspaceSummary, 0644)
}

func (ws *Workspace) pipelineManifestPath() (string, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return "", err
	}
	pipelineManifestPath := filepath.Join(copilotPath, pipelineFileName)
	return pipelineManifestPath, nil
}

func (ws *Workspace) summaryPath() (string, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return "", err
	}
	workspaceSummaryPath := filepath.Join(copilotPath, SummaryFileName)
	return workspaceSummaryPath, nil
}

func (ws *Workspace) createCopilotDir() error {
	// First check to see if a manifest directory already exists
	existingWorkspace, _ := ws.CopilotDirPath()
	if existingWorkspace != "" {
		return nil
	}
	return ws.fsUtils.Mkdir(CopilotDirName, 0755)
}

// CopilotDirPath returns the absolute path to the workspace's copilot dir.
func (ws *Workspace) CopilotDirPath() (string, error) {
	if ws.copilotDir != "" {
		return ws.copilotDir, nil
	}
	// Are we in the application directory?
	inCopilotDir := filepath.Base(ws.workingDir) == CopilotDirName
	if inCopilotDir {
		ws.copilotDir = ws.workingDir
		return ws.copilotDir, nil
	}

	searchingDir := ws.workingDir
	for try := 0; try < maximumParentDirsToSearch; try++ {
		currentDirectoryPath := filepath.Join(searchingDir, CopilotDirName)
		inCurrentDirPath, err := ws.fsUtils.DirExists(currentDirectoryPath)
		if err != nil {
			return "", err
		}
		if inCurrentDirPath {
			ws.copilotDir = currentDirectoryPath
			return ws.copilotDir, nil
		}
		searchingDir = filepath.Dir(searchingDir)
	}
	return "", &errWorkspaceNotFound{
		CurrentDirectory:      ws.workingDir,
		ManifestDirectoryName: CopilotDirName,
		NumberOfLevelsChecked: maximumParentDirsToSearch,
	}
}

func (ws *Workspace) readWorkloadType(dat []byte) (string, error) {
	wl := struct {
		Type string `yaml:"type"`
	}{}
	if err := yaml.Unmarshal(dat, &wl); err != nil {
		return "", err
	}
	return wl.Type, nil
}

// write flushes the data to a file under the copilot directory joined by path elements.
func (ws *Workspace) write(data []byte, elem ...string) (string, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return "", err
	}
	pathElems := append([]string{copilotPath}, elem...)
	filename := filepath.Join(pathElems...)

	if err := ws.fsUtils.MkdirAll(filepath.Dir(filename), 0755 /* -rwxr-xr-x */); err != nil {
		return "", fmt.Errorf("create directories for file %s: %w", filename, err)
	}
	exist, err := ws.fsUtils.Exists(filename)
	if err != nil {
		return "", fmt.Errorf("check if manifest file %s exists: %w", filename, err)
	}
	if exist {
		return "", &ErrFileExists{FileName: filename}
	}
	if err := ws.fsUtils.WriteFile(filename, data, 0644 /* -rw-r--r-- */); err != nil {
		return "", fmt.Errorf("write manifest file: %w", err)
	}
	return filename, nil
}

// read returns the contents of the file under the copilot directory joined by path elements.
func (ws *Workspace) read(elem ...string) ([]byte, error) {
	copilotPath, err := ws.CopilotDirPath()
	if err != nil {
		return nil, err
	}
	pathElems := append([]string{copilotPath}, elem...)
	return ws.fsUtils.ReadFile(filepath.Join(pathElems...))
}

// ListDockerfiles returns the list of Dockerfiles within the current
// working directory and a sub-directory level below. If an error occurs while
// reading directories, or no Dockerfiles found returns the error.
func (ws *Workspace) ListDockerfiles() ([]string, error) {
	wdFiles, err := ws.fsUtils.ReadDir(ws.workingDir)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}
	var directories []string
	for _, wdFile := range wdFiles {
		// Add current directory if a Dockerfile exists, otherwise continue.
		if !wdFile.IsDir() {
			if wdFile.Name() == dockerfileName {
				directories = append(directories, filepath.Dir(wdFile.Name()))
			}
			continue
		}

		// Add sub-directories containing a Dockerfile one level below current directory.
		subFiles, err := ws.fsUtils.ReadDir(wdFile.Name())
		if err != nil {
			return nil, fmt.Errorf("read directory: %w", err)
		}
		for _, f := range subFiles {
			// NOTE: ignore directories in sub-directories.
			if f.IsDir() {
				continue
			}

			if f.Name() == dockerfileName {
				directories = append(directories, wdFile.Name())
			}
		}
	}
	if len(directories) == 0 {
		return nil, &ErrDockerfileNotFound{
			dir: ws.workingDir,
		}
	}
	sort.Strings(directories)
	dockerfiles := make([]string, 0, len(directories))
	for _, dir := range directories {
		file := dir + "/" + dockerfileName
		dockerfiles = append(dockerfiles, file)
	}
	return dockerfiles, nil
}

// ErrDockerfileNotFound is returned when no Dockerfiles could be found in the current
// working directory or in any directories one level down from it.
type ErrDockerfileNotFound struct {
	dir string
}

func (e *ErrDockerfileNotFound) Error() string {
	return fmt.Sprintf("no Dockerfiles found within %s or a sub-directory level below", e.dir)
}

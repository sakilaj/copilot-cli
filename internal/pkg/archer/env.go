// Copyright 2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package archer contains the structs that represent archer concepts, and the associated interfaces to manipulate them.
package archer

// Environment represents the configuration of a particular Environment in a Project. It includes
// the location of the Environment (account and region), the name of the environment, as well as the project
// the environment belongs to.
type Environment struct {
	Project     string `json:"project" yaml:"-"`     // Name of the project this environment belongs to.
	Name        string `json:"name" yaml:"name"`     // Name of the environment, must be unique within a project.
	Region      string `json:"region" yaml:"-"`      // Name of the region this environment is stored in.
	AccountID   string `json:"accountID" yaml:"-"`   // Account ID of the account this environment is stored in.
	Prod        bool   `json:"prod" yaml:"-"`        // Whether or not this environment is a production environment.
	RegistryURL string `json:"registryURL" yaml:"-"` // URL For ECR Registry for this environment.
}

// DeployEnvironmentInput represents the fields required to setup and deploy an environment
type DeployEnvironmentInput struct {
	Project                  string // Name of the project this environment belongs to.
	Name                     string // Name of the environment, must be unique within a project.
	Prod                     bool   // Whether or not this environment is a production environment.
	PublicLoadBalancer       bool   // Whether or not this environment should contain a shared public load balancer between applications.
	ToolsAccountPrincipalARN string // The Principal ARN of the tools account.
}

// EnvironmentStore can List, Create and Get environments in an underlying project management store
type EnvironmentStore interface {
	EnvironmentLister
	EnvironmentGetter
	EnvironmentCreator
}

// EnvironmentLister fetches and returns a list of environments from an underlying project management store
type EnvironmentLister interface {
	ListEnvironments(projectName string) ([]*Environment, error)
}

// EnvironmentGetter fetches and returns an environment from an underlying project management store
type EnvironmentGetter interface {
	GetEnvironment(projectName string, environmentName string) (*Environment, error)
}

// EnvironmentCreator creates an environment in the underlying project management store
type EnvironmentCreator interface {
	CreateEnvironment(env *Environment) error
}

// EnvironmentDeployer can deploy an environment
type EnvironmentDeployer interface {
	DeployEnvironment(env *DeployEnvironmentInput) error
	WaitForEnvironmentCreation(env *DeployEnvironmentInput) (*Environment, error)
}

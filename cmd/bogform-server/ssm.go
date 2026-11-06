// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ssmEnvironment loads every parameter under prefix as a configuration value, so that secrets don't
// have to be in the function's environment or in deployment state. The parameter /prefix/database-url
// becomes BOG_SERVER_DATABASE_URL. Environment variables take priority.
func ssmEnvironment(ctx context.Context, prefix string) (func(string) string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	client := ssm.NewFromConfig(cfg)
	prefix = "/" + strings.Trim(prefix, "/") + "/"

	values := map[string]string{}
	paginator := ssm.NewGetParametersByPathPaginator(client, &ssm.GetParametersByPathInput{
		Path:           aws.String(prefix),
		Recursive:      aws.Bool(false),
		WithDecryption: aws.Bool(true),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading parameters under %s: %w", prefix, err)
		}
		for _, parameter := range page.Parameters {
			name := strings.TrimPrefix(aws.ToString(parameter.Name), prefix)
			key := "BOG_SERVER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
			values[key] = aws.ToString(parameter.Value)
		}
	}
	return func(name string) string {
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
		return values[name]
	}, nil
}

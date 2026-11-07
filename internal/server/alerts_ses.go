// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// sesSender sends email with Amazon SES, in the region of the server's AWS credentials. The client is
// created on first use, so that servers without email alerts don't need AWS configuration.
type sesSender struct {
	once   sync.Once
	client *sesv2.Client
	err    error
}

func (s *sesSender) Send(ctx context.Context, from string, to []string, subject, body string) error {
	s.once.Do(func() {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			s.err = fmt.Errorf("loading AWS configuration: %w", err)
			return
		}
		s.client = sesv2.NewFromConfig(cfg)
	})
	if s.err != nil {
		return s.err
	}
	_, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination:      &types.Destination{ToAddresses: to},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String(body), Charset: aws.String("UTF-8")}},
		}},
	})
	return err
}

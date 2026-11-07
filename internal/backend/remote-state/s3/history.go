// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package s3

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/bogware/bogform/internal/backend"
)

var _ backend.StateHistory = (*Backend)(nil)

// StateVersions lists the versions of a workspace's state object. It needs bucket versioning: without
// it, S3 keeps only the latest version.
func (b *Backend) StateVersions(ctx context.Context, workspace string, limit int) ([]backend.StateVersion, error) {
	key := b.path(workspace)
	ctx, _ = attachLoggerToContext(ctx)

	versioning, err := b.s3Client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(b.bucketName)})
	if err != nil {
		return nil, fmt.Errorf("couldn't check versioning on the bucket %q: %w", b.bucketName, err)
	}
	if versioning.Status == "" {
		return nil, fmt.Errorf("versioning isn't enabled on the bucket %q, so S3 keeps only the latest state. Enable it to keep earlier versions from now on", b.bucketName)
	}

	var versions []backend.StateVersion
	paginator := s3.NewListObjectVersionsPaginator(b.s3Client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(b.bucketName),
		Prefix: aws.String(key),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("couldn't list versions of %q: %w", key, err)
		}
		for _, v := range page.Versions {
			if aws.ToString(v.Key) != key {
				continue // Another object that shares the prefix.
			}
			versions = append(versions, backend.StateVersion{
				ID:      aws.ToString(v.VersionId),
				Time:    aws.ToTime(v.LastModified).UTC(),
				Size:    aws.ToInt64(v.Size),
				Current: aws.ToBool(v.IsLatest),
			})
		}
		for _, m := range page.DeleteMarkers {
			if aws.ToString(m.Key) != key {
				continue
			}
			versions = append(versions, backend.StateVersion{
				ID:          aws.ToString(m.VersionId),
				Time:        aws.ToTime(m.LastModified).UTC(),
				Size:        -1,
				Description: "deleted",
			})
		}
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Time.After(versions[j].Time) })
	if limit > 0 && len(versions) > limit {
		versions = versions[:limit]
	}
	return versions, nil
}

// StateVersionData downloads one version of a workspace's state object.
func (b *Backend) StateVersionData(ctx context.Context, workspace, id string) ([]byte, error) {
	if id == "" {
		return nil, backend.ErrStateVersionNotFound
	}
	ctx, _ = attachLoggerToContext(ctx)
	client, err := b.remoteClient(workspace)
	if err != nil {
		return nil, err
	}
	input := &s3.GetObjectInput{
		Bucket:    aws.String(b.bucketName),
		Key:       aws.String(client.path),
		VersionId: aws.String(id),
	}
	if client.serverSideEncryption && client.customerEncryptionKey != nil {
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(client.customerEncryptionKey))
		input.SSECustomerAlgorithm = aws.String(s3EncryptionAlgorithm)
		input.SSECustomerKeyMD5 = aws.String(client.getSSECustomerKeyMD5())
	}
	output, err := b.s3Client.GetObject(ctx, input, s3optDisableDefaultChecksum(b.skipS3Checksum))
	if err != nil {
		var noKey *types.NoSuchKey
		if errors.As(err, &noKey) {
			return nil, backend.ErrStateVersionNotFound
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NoSuchVersion", "InvalidArgument":
				return nil, backend.ErrStateVersionNotFound
			case "MethodNotAllowed":
				// S3 answers this way when the version is a delete marker.
				return nil, backend.ErrStateVersionDeleted
			}
		}
		return nil, fmt.Errorf("couldn't download version %q of %q: %w", id, client.path, err)
	}
	defer output.Body.Close()
	return io.ReadAll(output.Body)
}

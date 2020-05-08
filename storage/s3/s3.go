package s3

import (
	"fmt"
	"io"
	"strings"

	"github.com/drone/drone-cache-lib/storage"
	"github.com/dustin/go-humanize"
	"github.com/minio/minio-go"
	"github.com/minio/minio-go/pkg/credentials"
	log "github.com/sirupsen/logrus"
)

// Options contains configuration for the S3 connection.
type Options struct {
	Endpoint            string
	AcceleratedEndpoint string
	Key                 string
	Secret              string
	Access              string
	Token               string

	// us-east-1
	// us-west-1
	// us-west-2
	// eu-west-1
	// ap-southeast-1
	// ap-southeast-2
	// ap-northeast-1
	// sa-east-1
	Region string

	UseSSL bool
}

type s3Storage struct {
	client *minio.Client
	opts   *Options
}

// New method creates an implementation of Storage with S3 as the backend.
func New(opts *Options) (storage.Storage, error) {
	var creds *credentials.Credentials
	if len(opts.Access) != 0 && len(opts.Secret) != 0 {
		creds = credentials.NewStaticV4(opts.Access, opts.Secret, opts.Token)
	} else {
		creds = credentials.NewIAM("")

		// See if the IAM role can be retrieved
		_, err := creds.Get()
		if err != nil {
			return nil, err
		}
	}
	client, err := minio.NewWithCredentials(opts.Endpoint, creds, opts.UseSSL, "")

	if err != nil {
		return nil, err
	}

	if opts.AcceleratedEndpoint != "" {
		client.SetS3TransferAccelerate(opts.AcceleratedEndpoint)
	}

	return &s3Storage{
		client: client,
		opts:   opts,
	}, nil
}

func (s *s3Storage) Get(p string, dst io.Writer) error {
	bucket, key := splitBucket(p)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("Invalid path %s", p)
	}

	log.Debugf("Retrieving file in %s at %s", bucket, key)

	exists, err := s.client.BucketExists(bucket)

	if !exists {
		return err
	}

	object, err := s.client.GetObject(bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return err
	}

	log.Debugf("Copying object from the server")

	numBytes, err := io.Copy(dst, object)

	if err != nil {
		return err
	}

	log.Debugf("Downloaded %s from server", humanize.Bytes(uint64(numBytes)))

	return nil
}

func (s *s3Storage) Put(p string, src io.Reader) error {
	bucket, key := splitBucket(p)

	log.Debugf("Uploading to bucket %s at %s", bucket, key)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("Invalid path %s", p)
	}

	exists, err := s.client.BucketExists(bucket)
	if err != nil {
		return err
	}

	if !exists {
		if err = s.client.MakeBucket(bucket, s.opts.Region); err != nil {
			return err
		}
		log.Debugf("Bucket %s created", bucket)
	} else {
		log.Debugf("Bucket %s already exists", bucket)
	}

	log.Debugf("Putting file in %s at %s", bucket, key)

	numBytes, err := s.client.PutObject(bucket, key, src, -1, minio.PutObjectOptions{ContentType: "application/tar"})

	if err != nil {
		return err
	}

	log.Debugf("Uploaded %s to server", humanize.Bytes(uint64(numBytes)))

	return nil
}

func (s *s3Storage) List(p string) ([]storage.FileEntry, error) {
	bucket, key := splitBucket(p)

	log.Debugf("Retrieving objects in bucket %s at %s", bucket, key)

	if len(bucket) == 0 || len(key) == 0 {
		return nil, fmt.Errorf("Invalid path %s", p)
	}

	exists, err := s.client.BucketExists(bucket)

	if err != nil {
		return nil, fmt.Errorf("%s does not exist: %s", p, err)
	}
	if !exists {
		return nil, fmt.Errorf("%s does not exist", p)
	}

	// Create a done channel to control 'ListObjectsV2' go routine.
	doneCh := make(chan struct{})

	// Indicate to our routine to exit cleanly upon return.
	defer close(doneCh)

	var objects []storage.FileEntry
	isRecursive := true
	objectCh := s.client.ListObjectsV2(bucket, key, isRecursive, doneCh)
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("Failed to retrieve object %s: %s", object.Key, object.Err)
		}

		path := bucket + "/" + object.Key
		objects = append(objects, storage.FileEntry{
			Path:         path,
			Size:         object.Size,
			LastModified: object.LastModified,
		})
		log.Debugf("Found object %s: Path=%s Size=%d LastModified=%s", object.Key, path, object.Size, object.LastModified)
	}

	log.Debugf("Found %d objects in bucket %s at %s", len(objects), bucket, key)

	return objects, nil
}

func (s *s3Storage) Delete(p string) error {
	bucket, key := splitBucket(p)

	log.Debugf("Deleting object in bucket %s at %s", bucket, key)

	if len(bucket) == 0 || len(key) == 0 {
		return fmt.Errorf("Invalid path %s", p)
	}

	exists, err := s.client.BucketExists(bucket)

	if err != nil {
		return fmt.Errorf("%s does not exist: %s", p, err)
	}
	if !exists {
		return fmt.Errorf("%s does not exist", p)
	}

	err = s.client.RemoveObject(bucket, key)
	return err
}

func splitBucket(p string) (string, string) {
	// Remove initial forward slash
	full := strings.TrimPrefix(p, "/")

	// Get first index
	i := strings.Index(full, "/")

	if i != -1 && len(full) != i+1 {
		// Bucket names need to be all lower case for the key it doesnt matter
		return strings.ToLower(full[0:i]), full[i+1:]
	}

	return "", ""
}

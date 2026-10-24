package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/DejavuMoe/Pail/internal/cloud"
	"github.com/DejavuMoe/Pail/internal/mcsmanager"
	"github.com/DejavuMoe/Pail/internal/storage"
)

// BackendDescriptor is what a backend plugin declares to the rest of the
// application: its provider ID and the operations it implements.
type BackendDescriptor struct {
	ID           string
	Capabilities CapabilitySet
}

// Backend is a storage backend plugin. Every provider an account can name is
// registered here; account validation and provider lookup go through the
// registry, never through a switch on the provider string. A backend may also
// implement StorageFactory and/or CloudFactory to build protocol clients.
type Backend interface {
	Descriptor() BackendDescriptor
	// NormalizeAccount validates and defaults the fields this backend owns.
	// Shared account validation (IDs, URLs, credentials) runs before it.
	NormalizeAccount(*Account) error
}

// StorageFactory is implemented by backends that speak the S3 protocol
// through internal/storage.
type StorageFactory interface {
	NewStorage(ctx context.Context, cfg storage.Config) (*storage.Client, error)
}

// CloudFactory is implemented by backends that provide AWS cloud services
// (currently CloudFront) through internal/cloud.
type CloudFactory interface {
	NewCloud(ctx context.Context, cfg storage.Config, opts ...cloud.Option) (*cloud.Client, error)
}

// Tester is implemented by backends whose connection test is not an S3 call.
// The backend owns the request; returning an *OpError keeps its kind.
type Tester interface {
	TestConnection(ctx context.Context, a Account, sec Secrets) error
}

// backendRegistry holds the registered plugins by provider ID. Registration
// happens once at startup; the map is never written afterwards.
var backendRegistry = map[string]Backend{}

func init() {
	registerBackend(awsBackend{})
	registerBackend(s3Backend{})
	registerBackend(mcsBackend{})
}

// registerBackend adds b to the registry. A duplicate or empty descriptor is a
// programming error and panics at startup.
func registerBackend(b Backend) {
	d := b.Descriptor()
	if d.ID == "" {
		panic("app: backend plugin has no ID")
	}
	if len(d.Capabilities) == 0 {
		panic("app: backend " + d.ID + " declares no capabilities")
	}
	if _, dup := backendRegistry[d.ID]; dup {
		panic("app: backend " + d.ID + " is registered twice")
	}
	backendRegistry[d.ID] = b
}

// Backends returns every registered backend, sorted by provider ID. The
// capability sets are copies; callers may keep or modify them.
func Backends() []BackendDescriptor {
	out := make([]BackendDescriptor, 0, len(backendRegistry))
	for _, b := range backendRegistry {
		d := b.Descriptor()
		d.Capabilities = d.Capabilities.Clone()
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// backendFor returns the registered plugin for provider. It is the only way
// the rest of the package resolves a provider.
func backendFor(provider string) (Backend, error) {
	b, ok := backendRegistry[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	return b, nil
}

// providerCapabilities returns the capabilities declared by provider. The
// result is the plugin's own set and must not be modified.
func providerCapabilities(provider string) CapabilitySet {
	if b, ok := backendRegistry[provider]; ok {
		return b.Descriptor().Capabilities
	}
	return nil
}

// ProviderHasCapability reports whether provider declares c. It is the
// UI-facing form of providerCapabilities (no mutable set escapes).
func ProviderHasCapability(provider string, c Capability) bool {
	return providerCapabilities(provider).Has(c)
}

// Capabilities returns the capabilities of the backend behind accountID. An
// unknown account has none. The returned set is a copy.
func (s *Service) Capabilities(accountID string) CapabilitySet {
	s.mu.Lock()
	rec, ok := s.accountLocked(accountID)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return providerCapabilities(rec.Provider).Clone()
}

// HasCapability reports whether the backend behind accountID declares c.
func (s *Service) HasCapability(accountID string, c Capability) bool {
	s.mu.Lock()
	rec, ok := s.accountLocked(accountID)
	s.mu.Unlock()
	return ok && providerCapabilities(rec.Provider).Has(c)
}

// requireCapability fails with Unsupported when the account's backend does not
// declare c. It is called by every service operation that needs a capability,
// after the request's own validation, so that a UI shortcut, the CLI or a
// future plugin cannot bypass what the UI hides.
func (s *Service) requireCapability(op, accountID string, c Capability) error {
	s.mu.Lock()
	rec, ok := s.accountLocked(accountID)
	s.mu.Unlock()
	if !ok {
		return opErr(op, ErrNotFound, "no account %q", accountID)
	}
	if !providerCapabilities(rec.Provider).Has(c) {
		return opErr(op, ErrUnsupported, "the %s backend does not support %s", rec.Provider, c)
	}
	return nil
}

// ---- built-in plugins ----------------------------------------------------

var s3Capabilities = NewCapabilitySet(
	CapBucketList, CapBucketCreate, CapBucketDelete, CapBucketEmpty, CapBucketConfig,
	CapObjectList, CapObjectRead, CapObjectUpload, CapObjectDownload,
	CapObjectCopy, CapObjectMove, CapObjectRename, CapObjectDelete,
	CapObjectFolderCreate, CapObjectProperties, CapObjectTags, CapObjectACL,
	CapObjectStorageClass, CapObjectEncryption, CapObjectPresign, CapObjectVersions,
	CapMultipart, CapSync,
)

var awsCapabilities = s3Capabilities.With(CapCloudFront)

// s3Backend is the generic S3-compatible plugin. It shares the S3 protocol
// layer with AWS but speaks no AWS-only service.
type s3Backend struct{}

func (s3Backend) Descriptor() BackendDescriptor {
	return BackendDescriptor{ID: ProviderS3, Capabilities: s3Capabilities}
}

func (s3Backend) NormalizeAccount(a *Account) error {
	if a.Endpoint == "" {
		return errors.New("an endpoint is required for S3-compatible providers")
	}
	return nil
}

func (s3Backend) NewStorage(ctx context.Context, cfg storage.Config) (*storage.Client, error) {
	return storage.New(ctx, cfg)
}

// awsBackend is the Amazon S3 plugin: the S3 protocol layer plus CloudFront.
type awsBackend struct{}

func (awsBackend) Descriptor() BackendDescriptor {
	return BackendDescriptor{ID: ProviderAWS, Capabilities: awsCapabilities}
}

func (awsBackend) NormalizeAccount(*Account) error { return nil }

func (awsBackend) NewStorage(ctx context.Context, cfg storage.Config) (*storage.Client, error) {
	return storage.New(ctx, cfg)
}

func (awsBackend) NewCloud(ctx context.Context, cfg storage.Config, opts ...cloud.Option) (*cloud.Client, error) {
	return cloud.New(ctx, cfg, opts...)
}

// mcsCapabilities are the MCSManager file manager operations Pail implements.
var mcsCapabilities = NewCapabilitySet(
	CapMCSInstanceList,
	CapMCSFileList, CapMCSFileStatus, CapMCSFileDownload, CapMCSFileUpload,
	CapMCSFileDelete, CapMCSFileMkdir, CapMCSFileTouch, CapMCSFileCopy, CapMCSFileMove,
	CapMCSFileChmod, CapMCSFileArchive, CapMCSFileText,
)

// mcsBackend is the MCSManager panel plugin. It speaks the panel's HTTP API,
// not S3, and therefore declares only the mcs.* capabilities.
type mcsBackend struct{}

func (mcsBackend) Descriptor() BackendDescriptor {
	return BackendDescriptor{ID: ProviderMCS, Capabilities: mcsCapabilities}
}

func (mcsBackend) NormalizeAccount(a *Account) error {
	if a.Endpoint == "" {
		return errors.New("an MCSManager panel URL is required")
	}
	if a.Credentials != CredAPIKey {
		return errors.New("MCSManager accounts use an API key")
	}
	if a.RoleARN != "" || a.ExternalID != "" || a.RoleSessionName != "" {
		return errors.New("role settings do not apply to MCSManager")
	}
	if a.CDNURL != "" || len(a.CDNBuckets) > 0 {
		return errors.New("CDN settings do not apply to MCSManager")
	}
	return nil
}

// TestConnection lists nodes, falling back to the global instance search for
// API keys that are not administrators but can still see their instances.
func (mcsBackend) TestConnection(ctx context.Context, a Account, sec Secrets) error {
	const op = "TestAccount"
	c, err := mcsmanager.New(mcsmanager.Config{
		Endpoint:           a.Endpoint,
		APIKey:             sec.SecretAccessKey,
		InsecureSkipVerify: a.InsecureSkipVerify,
	})
	if err != nil {
		return invalid(op, "%v", err)
	}
	if _, err := c.ListDaemons(ctx); err == nil {
		return nil
	} else if !isMCSAccessDenied(err) {
		return mapMCS(op, err)
	}
	if _, err := c.ListAllInstances(ctx, 1, 1); err != nil {
		return mapMCS(op, err)
	}
	return nil
}

func isMCSAccessDenied(err error) bool {
	var ae *mcsmanager.APIError
	return errors.As(err, &ae) && ae.AccessDenied()
}

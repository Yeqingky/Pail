package app

import (
	"context"
	"testing"
)

func TestBackendsDeclareCapabilities(t *testing.T) {
	bs := Backends()
	if len(bs) != 3 {
		t.Fatalf("backends = %v, want aws, mcsmanager and s3", bs)
	}
	byID := map[string]CapabilitySet{}
	for _, b := range bs {
		if len(b.Capabilities) == 0 {
			t.Errorf("%s declares no capabilities", b.ID)
		}
		byID[b.ID] = b.Capabilities
	}
	aws, s3, mcs := byID[ProviderAWS], byID[ProviderS3], byID[ProviderMCS]
	if aws == nil || s3 == nil || mcs == nil {
		t.Fatalf("missing built-in backend: %v", byID)
	}
	if !mcs.Has(CapMCSInstanceList) || !mcs.Has(CapMCSFileList) || !mcs.Has(CapMCSFileDownload) || !mcs.Has(CapMCSFileUpload) {
		t.Error("mcsmanager lacks its file manager capabilities")
	}
	if mcs.Has(CapObjectList) || mcs.Has(CapBucketList) {
		t.Error("mcsmanager declares S3 capabilities")
	}
	if !aws.Has(CapCloudFront) {
		t.Error("aws must declare cloudfront")
	}
	if s3.Has(CapCloudFront) {
		t.Error("s3 must not declare cloudfront")
	}
	for _, c := range []Capability{
		CapBucketList, CapBucketCreate, CapBucketDelete, CapBucketEmpty, CapBucketConfig,
		CapObjectList, CapObjectRead, CapObjectUpload, CapObjectDownload, CapObjectCopy,
		CapObjectMove, CapObjectRename, CapObjectDelete, CapObjectFolderCreate,
		CapObjectProperties, CapObjectTags, CapObjectACL, CapObjectStorageClass,
		CapObjectEncryption, CapObjectPresign, CapObjectVersions, CapMultipart, CapSync,
	} {
		if !s3.Has(c) {
			t.Errorf("s3 does not declare %s", c)
		}
		if !aws.Has(c) {
			t.Errorf("aws does not declare %s", c)
		}
	}
}

func TestProviderCapabilitiesAreUnknownForAnUnregisteredProvider(t *testing.T) {
	if ProviderHasCapability("nope", CapObjectList) {
		t.Fatal("an unregistered provider declared a capability")
	}
	if _, err := normalizeAccount(Account{Name: "x", Provider: "nope", Credentials: CredDefault}); err == nil {
		t.Fatal("normalizeAccount accepted an unregistered provider")
	}
}

func TestServiceCapabilitiesFollowTheBackend(t *testing.T) {
	s := newTestService(t, nil)
	s3a, err := s.SaveAccount(staticAccount("s3", "http://h"), &Secrets{SecretAccessKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasCapability(s3a.ID, CapObjectUpload) || !s.HasCapability(s3a.ID, CapObjectVersions) {
		t.Fatal("s3 account lacks object capabilities")
	}
	if s.HasCapability(s3a.ID, CapCloudFront) {
		t.Fatal("s3 account declares cloudfront")
	}
	if caps := s.Capabilities(s3a.ID); caps.Has(CapCloudFront) {
		t.Fatal("Capabilities returned cloudfront for s3")
	}
	if caps := s.Capabilities(s3a.ID); !caps.Has(CapBucketList) {
		t.Fatal("Capabilities lost bucket.list for s3")
	}

	awsa, err := s.SaveAccount(Account{Name: "aws", Provider: ProviderAWS, Credentials: CredDefault}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasCapability(awsa.ID, CapCloudFront) {
		t.Fatal("aws account does not declare cloudfront")
	}

	if err := s.requireCapability("Test", s3a.ID, CapCloudFront); !IsKind(err, ErrUnsupported) {
		t.Fatalf("requireCapability on s3 cloudfront = %v, want Unsupported", err)
	}
	if err := s.requireCapability("Test", awsa.ID, CapCloudFront); err != nil {
		t.Fatalf("requireCapability on aws cloudfront = %v", err)
	}
	if err := s.requireCapability("Test", "missing", CapObjectList); !IsKind(err, ErrNotFound) {
		t.Fatalf("requireCapability on a missing account = %v, want NotFound", err)
	}
}

func TestServiceRejectsACapabilityTheBackendLacks(t *testing.T) {
	s := newTestService(t, nil)
	a, err := s.SaveAccount(staticAccount("s3", "http://h"), &Secrets{SecretAccessKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Distributions(context.Background(), a.ID)
	if !IsKind(err, ErrUnsupported) {
		t.Fatalf("Distributions on s3 = %v, want Unsupported", err)
	}
}

package main

import (
	"testing"

	core "github.com/DejavuMoe/Pail/internal/app"
)

func TestS3AccountHidesCloudFront(t *testing.T) {
	h := newHarness(t, "light")
	if got := h.a.account().Provider; got != core.ProviderS3 {
		t.Fatalf("harness account provider = %q, want s3", got)
	}
	if h.a.cfAvailable() {
		t.Fatal("S3 account offers CloudFront")
	}
	if h.a.visible(h.a.cmd("tool.cloudfront")) {
		t.Fatal("the CloudFront tool is visible for S3")
	}
	if h.a.visible(h.a.cmd("invalidate")) {
		t.Fatal("the invalidation command is visible for S3")
	}
	if !h.a.visible(h.a.cmd("tool.sync")) {
		t.Fatal("the sync tool is hidden for S3")
	}
	h.settle()
	h.mustNotHave("CloudFront")

	h.a.accounts[0].Provider = core.ProviderAWS
	h.settle()
	if !h.a.cfAvailable() {
		t.Fatal("AWS account does not offer CloudFront")
	}
	if !h.a.visible(h.a.cmd("tool.cloudfront")) {
		t.Fatal("the CloudFront tool is hidden for AWS")
	}
	h.mustHave("CloudFront")
}

func TestBackendWithoutCapabilitiesHidesItsSurfaces(t *testing.T) {
	h := newHarness(t, "light")
	h.a.accounts[0].Provider = "limited"
	h.settle()

	for _, id := range []string{
		"upload.files", "upload.folder", "download", "newfolder",
		"copy", "cut", "paste", "rename", "delete",
		"restore", "restoreall", "copysigned", "storageclass", "encryption",
		"newbucket", "bucketsettings", "tool.sync", "tool.cloudfront", "tool.multipart",
	} {
		if h.a.visible(h.a.cmd(id)) {
			t.Errorf("%s is visible for a backend without capabilities", id)
		}
	}
	for _, id := range []string{"refresh", "palette", "inspector", "transfers", "tool.settings", "tool.rules", "tool.mime"} {
		if !h.a.visible(h.a.cmd(id)) {
			t.Errorf("%s is hidden although it needs no backend capability", id)
		}
	}
	for i := 1; i < len(inspectorTabs); i++ {
		if h.a.inspectorTabAllowed(i) {
			t.Errorf("inspector tab %q is visible for a backend without capabilities", inspectorTabs[i].name)
		}
	}
	if !h.a.inspectorTabAllowed(0) {
		t.Fatal("the Overview tab must stay visible")
	}
	h.mustNotHave("CloudFront", "Sync jobs", "Multipart uploads")
	h.mustHave("Settings", "Upload rules", "MIME types")
}

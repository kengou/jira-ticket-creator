// SPDX-License-Identifier: Apache-2.0

package jira

import "testing"

func TestMode_APIVersion(t *testing.T) {
	if got := ModeDataCenter.APIVersion(); got != "2" {
		t.Errorf("ModeDataCenter.APIVersion() = %q, want %q", got, "2")
	}
	if got := ModeCloud.APIVersion(); got != "3" {
		t.Errorf("ModeCloud.APIVersion() = %q, want %q", got, "3")
	}
}

func TestMode_String(t *testing.T) {
	if got := ModeDataCenter.String(); got != "Data Center" {
		t.Errorf("ModeDataCenter.String() = %q, want %q", got, "Data Center")
	}
	if got := ModeCloud.String(); got != "Cloud" {
		t.Errorf("ModeCloud.String() = %q, want %q", got, "Cloud")
	}
}

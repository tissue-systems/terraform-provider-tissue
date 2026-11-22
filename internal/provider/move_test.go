package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A tissue_mast_vital state saved by a provider that had no
// heartbeat_priority still moves to tissue_mast_heartbeat, and the priority
// comes across under both names.
func TestMoveFromAVitalStateWithoutHeartbeatPriority(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	target := schemas.ResourceSchemas["tissue_mast_heartbeat"].ValueType()

	saved := `{"id":"mc_1","name":"nightly-backup","url":"https://mast.tissue.dev/mk_1",` +
		`"default_priority":"normal","max_priority":"loud","sound":"","quiet_start":-1,"quiet_end":-1,` +
		`"quiet_tz":"","dedupe_window_secs":300,"storm_threshold":20,"storm_window_secs":300,` +
		`"repeat_secs":120,"expire_secs":3600,"escalate_after_secs":0,"ack_timeout_min":0,` +
		`"canary_url":"","policy_id":"","created_at":"2026-01-01T00:00:00.000Z","archived":false,` +
		`"period_secs":0,"grace_secs":1800,"schedule":"0 3 * * *","schedule_tz":"","max_duration_secs":0,` +
		`"vital_priority":"page","fail_threshold":1,"smart_early_warn":false,"host":"db1","paused":false}`

	move := func(from string) *tfprotov6.MoveResourceStateResponse {
		resp, err := server.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
			SourceProviderAddress: "registry.terraform.io/tissue-systems/tissue",
			SourceTypeName:        from,
			SourceState:           &tfprotov6.RawState{JSON: []byte(saved)},
			TargetTypeName:        "tissue_mast_heartbeat",
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := move("tissue_mast_vital")
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	if resp.TargetState == nil {
		t.Fatal("the move produced no state")
	}
	moved, err := resp.TargetState.Unmarshal(target)
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]tftypes.Value{}
	if err := moved.As(&attrs); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"id": "mc_1", "heartbeat_priority": "page", "vital_priority": "page", "schedule": "0 3 * * *",
	} {
		var got string
		if err := attrs[name].As(&got); err != nil || got != want {
			t.Errorf("%s is %q (%v), want %q", name, got, err, want)
		}
	}

	if resp := move("tissue_mast_channel"); len(resp.Diagnostics) == 0 {
		t.Error("a tissue_mast_channel moved into tissue_mast_heartbeat")
	}
}

// A tissue_mast_channel state moves to tissue_mast_alert_source as it is,
// and nothing else moves there.
func TestMoveFromAChannelState(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	target := schemas.ResourceSchemas["tissue_mast_alert_source"].ValueType()

	saved := `{"id":"mc_1","name":"deploys","url":"https://mast.tissue.dev/mk_1",` +
		`"default_priority":"loud","max_priority":"page","sound":"chirp","quiet_start":1320,"quiet_end":420,` +
		`"quiet_tz":"Europe/Berlin","dedupe_window_secs":300,"storm_threshold":20,"storm_window_secs":300,` +
		`"repeat_secs":120,"expire_secs":3600,"escalate_after_secs":0,"ack_timeout_min":30,` +
		`"canary_url":"","policy_id":"po_1","created_at":"2026-01-01T00:00:00.000Z","archived":false}`

	move := func(from string) *tfprotov6.MoveResourceStateResponse {
		resp, err := server.MoveResourceState(ctx, &tfprotov6.MoveResourceStateRequest{
			SourceProviderAddress: "registry.terraform.io/tissue-systems/tissue",
			SourceTypeName:        from,
			SourceState:           &tfprotov6.RawState{JSON: []byte(saved)},
			TargetTypeName:        "tissue_mast_alert_source",
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := move("tissue_mast_channel")
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	if resp.TargetState == nil {
		t.Fatal("the move produced no state")
	}
	moved, err := resp.TargetState.Unmarshal(target)
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]tftypes.Value{}
	if err := moved.As(&attrs); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"id": "mc_1", "url": "https://mast.tissue.dev/mk_1", "sound": "chirp", "max_priority": "page",
		"quiet_tz": "Europe/Berlin", "policy_id": "po_1",
	} {
		var got string
		if err := attrs[name].As(&got); err != nil || got != want {
			t.Errorf("%s is %q (%v), want %q", name, got, err, want)
		}
	}

	for _, from := range []string{"tissue_mast_vital", "tissue_mast_heartbeat", "tissue_mast_probe"} {
		if resp := move(from); len(resp.Diagnostics) == 0 {
			t.Errorf("a %s moved into tissue_mast_alert_source", from)
		}
	}
}

// A probe state saved before alert_source_id existed still reads, with the
// new attribute empty until the next refresh fills it in.
func TestAProbeStateWithoutAlertSourceIDStillReads(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	probe := schemas.ResourceSchemas["tissue_mast_probe"]
	saved := `{"id":"pr_1","name":"shop","kind":"http","target":"https://shop.acme.test/health","method":"GET",` +
		`"interval_secs":300,"expect":"up","expect_status":null,"expect_statuses":null,"expect_header":null,` +
		`"body_contains":null,"body_not_contains":null,"json_path":null,"expect_value":null,"expect_set":null,` +
		`"ignore_case":false,"resolver":null,"send":null,"request_headers":null,"request_body":null,` +
		`"request_content_type":null,"auth":null,"max_ms":null,"channel_id":"mc_1","policy_id":null,` +
		`"host":null,"paused":false,"domain_expiry":true,"created_at":"2026-01-01T00:00:00.000Z"}`
	resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "tissue_mast_probe",
		Version:  probe.Version,
		RawState: &tfprotov6.RawState{JSON: []byte(saved)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	state, err := resp.UpgradedState.Unmarshal(probe.ValueType())
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]tftypes.Value{}
	if err := state.As(&attrs); err != nil {
		t.Fatal(err)
	}
	var channel string
	if err := attrs["channel_id"].As(&channel); err != nil || channel != "mc_1" {
		t.Errorf("channel_id is %q (%v)", channel, err)
	}
	if !attrs["alert_source_id"].IsNull() {
		t.Errorf("alert_source_id is %v", attrs["alert_source_id"])
	}
}

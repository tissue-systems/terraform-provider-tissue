package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"tissue": providerserver.NewProtocol6WithError(New("test")()),
}

func providerBlock(f *fakeMast, token string) string {
	return fmt.Sprintf("provider \"tissue\" {\n  server = %q\n  token  = %q\n}\n", f.srv.URL, token)
}

func noChange(addr string) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)}}
}

func stateID(addr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return "", fmt.Errorf("%s not in state", addr)
		}
		return rs.Primary.ID, nil
	}
}

func TestAccChannel(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name             = "deploys"
  default_priority = "loud"
  sound            = "klaxon"
  ack_timeout_min  = 30
  canary_url       = "https://example.com/ok"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("tissue_mast_channel.deploys", "id", regexp.MustCompile(`^mc_`)),
					resource.TestMatchResourceAttr("tissue_mast_channel.deploys", "url", regexp.MustCompile(`^https://mast\.tissue\.dev/mk_`)),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "sound", "klaxon"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "max_priority", "loud"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "ack_timeout_min", "30"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "canary_url", "https://example.com/ok"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "repeat_secs", "120"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "quiet_tz", ""),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "policy_id", ""),
					func(*terraform.State) error {
						if got := str(f.resolveChannel("deploys").Sound); got != "klaxon.caf" {
							return fmt.Errorf("the server stores %q", got)
						}
						return nil
					},
				),
			},
			{
				// Someone raises the ceiling to page and changes the sound in
				// the app. Leaving those arguments out follows the app.
				PreConfig: func() {
					f.app("deploys", func(ch *fakeChannel) {
						ch.MaxPriority = "page"
						ch.Sound = ptr("chirp.caf")
						ch.QuietStart, ch.QuietEnd, ch.QuietTZ = 1320, 420, ptr("Europe/Berlin")
					})
				},
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name             = "deploys"
  default_priority = "loud"
  ack_timeout_min  = 30
  canary_url       = "https://example.com/ok"
}`,
				ConfigPlanChecks: noChange("tissue_mast_channel.deploys"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "max_priority", "page"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "sound", "chirp"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "quiet_tz", "Europe/Berlin"),
				),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name         = "deploys"
  max_priority = "loud"
}`,
				ExpectError: regexp.MustCompile(`can only be changed in the\s+Mast app`),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name         = "deploys"
  max_priority = "page"
  sound        = "chirp"
}`,
				ConfigPlanChecks: noChange("tissue_mast_channel.deploys"),
			},
			{
				// A rename keeps the id and the URL.
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name         = "deploys prod"
  max_priority = "page"
  sound        = "pager"
  repeat_secs  = 60
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "name", "deploys prod"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "sound", "pager"),
					resource.TestCheckResourceAttr("tissue_mast_channel.deploys", "repeat_secs", "60"),
					resource.TestMatchResourceAttr("tissue_mast_channel.deploys", "url", regexp.MustCompile(`^https://`)),
				),
			},
			{
				ResourceName:      "tissue_mast_channel.deploys",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "tissue_mast_channel.deploys",
				ImportState:       true,
				ImportStateId:     "deploys prod",
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccChannelRules(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config:      p + `resource "tissue_mast_channel" "x" { name = "Deploys" }`,
				ExpectError: regexp.MustCompile(`lowercase`),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "x" {
  name         = "deploys"
  max_priority = "page"
}`,
				ExpectError: regexp.MustCompile(`max_priority can't start at page`),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "x" {
  name       = "deploys"
  canary_url = " https://example.com"
}`,
				ExpectError: regexp.MustCompile(`trims`),
			},
		},
	})
}

func TestAccChannelGoneIsRecreated(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	config := p + `resource "tissue_mast_channel" "x" { name = "alerts" }`
	var first string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					first = s.RootModule().Resources["tissue_mast_channel.x"].Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					f.mu.Lock()
					delete(f.channels, first)
					f.mu.Unlock()
				},
				Config: config,
				Check: func(s *terraform.State) error {
					id := s.RootModule().Resources["tissue_mast_channel.x"].Primary.ID
					if id == first {
						return fmt.Errorf("still %s", id)
					}
					if f.resolveChannel("alerts") == nil {
						return fmt.Errorf("not recreated")
					}
					return nil
				},
			},
		},
	})
}

func TestAccScopeError(t *testing.T) {
	f := newFakeMast(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config:      providerBlock(f, readToken) + `resource "tissue_mast_channel" "x" { name = "alerts" }`,
				ExpectError: regexp.MustCompile(`needs\s+the mast:write scope`),
			},
		},
	})
}

func TestAccNoToken(t *testing.T) {
	t.Setenv("TISSUE_TOKEN", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config:      `resource "tissue_mast_channel" "x" { name = "alerts" }`,
				ExpectError: regexp.MustCompile(`No API token`),
			},
		},
	})
}

func TestAccTokenFromEnv(t *testing.T) {
	f := newFakeMast(t)
	t.Setenv("TISSUE_TOKEN", fullToken)
	t.Setenv("TISSUE_SERVER", f.srv.URL)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: `resource "tissue_mast_channel" "x" { name = "alerts" }`,
				Check:  resource.TestCheckResourceAttrSet("tissue_mast_channel.x", "url"),
			},
		},
	})
}

func TestAccHeartbeat(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_heartbeat" "backup" {
  name     = "nightly-backup"
  schedule = "0 3 * * *"
}`,
				ExpectError: regexp.MustCompile(`A schedule needs grace_secs`),
			},
			{
				Config: p + `
resource "tissue_mast_heartbeat" "backup" {
  name               = "nightly-backup"
  schedule           = "0 3 * * *"
  schedule_tz        = "America/Los_Angeles"
  grace_secs         = 1800
  host               = "db1"
  heartbeat_priority = "page"
  paused             = true
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "paused", "true"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "period_secs", "0"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "fail_threshold", "1"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "smart_early_warn", "false"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "max_priority", "loud"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "heartbeat_priority", "page"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "vital_priority", "page"),
					resource.TestCheckResourceAttrSet("tissue_mast_heartbeat.backup", "url"),
					func(*terraform.State) error {
						// The API knows a heartbeat as kind "vital".
						ch := f.resolveChannel("nightly-backup")
						if ch.Kind != "vital" || ch.VitalState != "paused" || ch.VitalPriority != "page" {
							return fmt.Errorf("server has kind %s, state %s, priority %s", ch.Kind, ch.VitalState, ch.VitalPriority)
						}
						return nil
					},
				),
			},
			{
				// Someone resumes it from the app and turns on early warnings.
				// With paused left out, that holds.
				PreConfig: func() {
					f.app("nightly-backup", func(ch *fakeChannel) {
						ch.VitalState = "late"
						ch.SmartEarlyWarn = true
					})
				},
				Config: p + `
resource "tissue_mast_heartbeat" "backup" {
  name               = "nightly-backup"
  schedule           = "0 3 * * *"
  schedule_tz        = "America/Los_Angeles"
  grace_secs         = 1800
  host               = "db1"
  heartbeat_priority = "page"
}`,
				ConfigPlanChecks: noChange("tissue_mast_heartbeat.backup"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "paused", "false"),
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.backup", "smart_early_warn", "true"),
				),
			},
			{
				ResourceName:      "tissue_mast_heartbeat.backup",
				ImportState:       true,
				ImportStateId:     "nightly-backup",
				ImportStateVerify: true,
			},
			{
				Config: p + `
resource "tissue_mast_heartbeat" "backup" {
  name               = "nightly-backup"
  schedule           = "0 3 * * *"
  schedule_tz        = "America/Los_Angeles"
  grace_secs         = 1800
  host               = "db1"
  heartbeat_priority = "page"
}

resource "tissue_mast_channel" "wrong" {
  name = "nightly-backup"
}`,
				ResourceName:  "tissue_mast_channel.wrong",
				ImportState:   true,
				ImportStateId: "nightly-backup",
				ExpectError:   regexp.MustCompile(`Import\s+it\s+as\s+tissue_mast_heartbeat`),
			},
		},
	})
}

// tissue_mast_vital is the deprecated name of tissue_mast_heartbeat, and
// vital_priority of heartbeat_priority. Both keep working, each priority
// argument moves the other, and a moved block carries the state to the
// documented type without touching the heartbeat on the server.
func TestAccVitalIsTheDeprecatedName(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	var id string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_vital" "queue" {
  name               = "queue-worker"
  period_secs        = 300
  vital_priority     = "page"
  heartbeat_priority = "page"
}`,
				ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination.*vital_priority`),
			},
			{
				Config: p + `
resource "tissue_mast_vital" "queue" {
  name           = "queue-worker"
  period_secs    = 300
  vital_priority = "page"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_vital.queue", "vital_priority", "page"),
					resource.TestCheckResourceAttr("tissue_mast_vital.queue", "heartbeat_priority", "page"),
					func(s *terraform.State) error {
						id = s.RootModule().Resources["tissue_mast_vital.queue"].Primary.ID
						if ch := f.resolveChannel("queue-worker"); ch.Kind != "vital" || ch.VitalPriority != "page" {
							return fmt.Errorf("server has kind %s, priority %s", ch.Kind, ch.VitalPriority)
						}
						return nil
					},
				),
			},
			{
				// The other spelling changes the same setting.
				Config: p + `
resource "tissue_mast_vital" "queue" {
  name               = "queue-worker"
  period_secs        = 300
  heartbeat_priority = "normal"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_vital.queue", "vital_priority", "normal"),
					resource.TestCheckResourceAttr("tissue_mast_vital.queue", "heartbeat_priority", "normal"),
				),
			},
			{
				Config: p + `
moved {
  from = tissue_mast_vital.queue
  to   = tissue_mast_heartbeat.queue
}

resource "tissue_mast_heartbeat" "queue" {
  name               = "queue-worker"
  period_secs        = 300
  heartbeat_priority = "normal"
}`,
				ConfigPlanChecks: noChange("tissue_mast_heartbeat.queue"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.queue", "heartbeat_priority", "normal"),
					func(s *terraform.State) error {
						if _, still := s.RootModule().Resources["tissue_mast_vital.queue"]; still {
							return fmt.Errorf("tissue_mast_vital.queue is still in state")
						}
						if got := s.RootModule().Resources["tissue_mast_heartbeat.queue"].Primary.ID; got != id {
							return fmt.Errorf("the heartbeat was %s and is %s after the move", id, got)
						}
						if n := len(f.channels); n != 1 {
							return fmt.Errorf("the server has %d channels after the move", n)
						}
						return nil
					},
				),
			},
			{
				// A deprecated argument on the documented type still works.
				Config: p + `
resource "tissue_mast_heartbeat" "queue" {
  name           = "queue-worker"
  period_secs    = 300
  vital_priority = "loud"
}

data "tissue_mast_channel" "queue" {
  name = tissue_mast_heartbeat.queue.name
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_heartbeat.queue", "heartbeat_priority", "loud"),
					resource.TestCheckResourceAttr("data.tissue_mast_channel.queue", "kind", "vital"),
					resource.TestCheckResourceAttr("data.tissue_mast_channel.queue", "heartbeat", "true"),
				),
			},
			{
				ResourceName: "tissue_mast_vital.old",
				Config: p + `
resource "tissue_mast_heartbeat" "queue" {
  name           = "queue-worker"
  period_secs    = 300
  vital_priority = "loud"
}

resource "tissue_mast_vital" "old" {
  name = "queue-worker"
}`,
				ImportState:        true,
				ImportStateId:      "queue-worker",
				ImportStatePersist: false,
			},
		},
	})
}

// tissue_mast_channel is the deprecated name of tissue_mast_alert_source. It
// keeps working, a moved block carries its state over without touching the
// alert source on the server, and the data source answers under both names.
func TestAccChannelIsTheDeprecatedName(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	var id, url string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_channel" "deploys" {
  name  = "deploys"
  sound = "chirp"
}`,
				Check: func(s *terraform.State) error {
					attrs := s.RootModule().Resources["tissue_mast_channel.deploys"].Primary.Attributes
					id, url = attrs["id"], attrs["url"]
					return nil
				},
			},
			{
				Config: p + `
moved {
  from = tissue_mast_channel.deploys
  to   = tissue_mast_alert_source.deploys
}

resource "tissue_mast_alert_source" "deploys" {
  name  = "deploys"
  sound = "chirp"
}

data "tissue_mast_alert_source" "deploys" {
  name = tissue_mast_alert_source.deploys.name
}

data "tissue_mast_channel" "deploys" {
  name = tissue_mast_alert_source.deploys.name
}`,
				ConfigPlanChecks: noChange("tissue_mast_alert_source.deploys"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.tissue_mast_alert_source.deploys", "kind", "channel"),
					resource.TestCheckResourceAttr("data.tissue_mast_alert_source.deploys", "heartbeat", "false"),
					resource.TestCheckResourceAttrPair("data.tissue_mast_channel.deploys", "id",
						"data.tissue_mast_alert_source.deploys", "id"),
					func(s *terraform.State) error {
						if _, still := s.RootModule().Resources["tissue_mast_channel.deploys"]; still {
							return fmt.Errorf("tissue_mast_channel.deploys is still in state")
						}
						attrs := s.RootModule().Resources["tissue_mast_alert_source.deploys"].Primary.Attributes
						if attrs["id"] != id || attrs["url"] != url {
							return fmt.Errorf("the alert source was %s and is %s after the move", id, attrs["id"])
						}
						if attrs["sound"] != "chirp" {
							return fmt.Errorf("sound is %q after the move", attrs["sound"])
						}
						if n := len(f.channels); n != 1 {
							return fmt.Errorf("the server has %d alert sources after the move", n)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "tissue_mast_alert_source.deploys",
				ImportState:       true,
				ImportStateId:     "deploys",
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccRotationAndOverrides(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	people := `
data "tissue_mast_person" "ana" { email = "ana@example.com" }
data "tissue_mast_person" "ben" { email = "ben@example.com" }
`
	rotation := func(handoff string) string {
		return fmt.Sprintf(`
resource "tissue_mast_rotation" "primary" {
  name    = "primary"
  %s
  tz      = "Europe/Berlin"
  order   = [data.tissue_mast_person.ana.user_id, data.tissue_mast_person.ben.user_id]
}
`, handoff)
	}
	overrides := func(n int, user string) string {
		return fmt.Sprintf(`
resource "tissue_mast_override" "holiday" {
  count       = %d
  rotation_id = tissue_mast_rotation.primary.id
  user_id     = %s
  start       = "2026-12-2${count.index + 1}"
  end         = "2026-12-2${count.index + 1}T18:00"
}
`, n, user)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + people + rotation(`handoff = "custom"
  length_hours = 12`) + overrides(3, "data.tissue_mast_person.ben.user_id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_rotation.primary", "length_hours", "12"),
					resource.TestCheckResourceAttr("tissue_mast_rotation.primary", "handoff_time", "09:00"),
					resource.TestCheckResourceAttr("tissue_mast_rotation.primary", "order.1", "usr_ben"),
					resource.TestCheckResourceAttr("tissue_mast_override.holiday.2", "start", "2026-12-23"),
					resource.TestMatchResourceAttr("tissue_mast_override.holiday.0", "override_id", regexp.MustCompile(`^ov_`)),
					func(*terraform.State) error {
						for _, sc := range f.schedules {
							if len(sc.Overrides) != 3 {
								return fmt.Errorf("the rotation has %d overrides, want 3", len(sc.Overrides))
							}
						}
						return nil
					},
				),
			},
			{
				// Leaving custom drops the shift length to 0 on the server,
				// and the plan says so instead of fighting it.
				Config: p + people + rotation(`handoff = "weekly"`) + overrides(2, "data.tissue_mast_person.ana.user_id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_rotation.primary", "handoff", "weekly"),
					resource.TestCheckResourceAttr("tissue_mast_rotation.primary", "length_hours", "0"),
					resource.TestCheckResourceAttr("tissue_mast_override.holiday.0", "user_id", "usr_ana"),
					resource.TestCheckResourceAttr("tissue_mast_override.holiday.1", "user_id", "usr_ana"),
					func(*terraform.State) error {
						for _, sc := range f.schedules {
							if len(sc.Overrides) != 2 {
								return fmt.Errorf("the rotation has %d overrides, want 2", len(sc.Overrides))
							}
						}
						return nil
					},
				),
			},
			{
				Config: p + people + rotation(`handoff = "weekly"`) + overrides(2, "data.tissue_mast_person.ana.user_id") + `
data "tissue_mast_rotation" "by_name" {
  name       = "primary"
  depends_on = [tissue_mast_rotation.primary]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.tissue_mast_rotation.by_name", "id", "tissue_mast_rotation.primary", "id"),
					resource.TestCheckResourceAttr("data.tissue_mast_rotation.by_name", "on_call_user_id", "usr_ana"),
					resource.TestCheckResourceAttr("data.tissue_mast_rotation.by_name", "tz", "Europe/Berlin"),
				),
			},
			{
				ResourceName:      "tissue_mast_rotation.primary",
				ImportState:       true,
				ImportStateId:     "primary",
				ImportStateVerify: true,
			},
			{
				ResourceName: "tissue_mast_override.holiday[1]",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["tissue_mast_override.holiday.1"]
					return "primary/" + rs.Primary.Attributes["override_id"], nil
				},
				ImportStateVerify: true,
			},
			{
				// An override someone deletes in the app is planned back in.
				PreConfig: func() {
					f.mu.Lock()
					for _, sc := range f.schedules {
						sc.Overrides = sc.Overrides[:1]
					}
					f.mu.Unlock()
				},
				Config: p + people + rotation(`handoff = "weekly"`) + overrides(2, "data.tissue_mast_person.ana.user_id"),
				Check: func(*terraform.State) error {
					for _, sc := range f.schedules {
						if len(sc.Overrides) != 2 {
							return fmt.Errorf("the rotation has %d overrides, want 2", len(sc.Overrides))
						}
					}
					return nil
				},
			},
		},
	})
}

func TestAccRotationRules(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_rotation" "r" {
  name    = "r"
  handoff = "custom"
}`,
				ExpectError: regexp.MustCompile(`A custom handoff needs length_hours`),
			},
			{
				Config: p + `
resource "tissue_mast_rotation" "r" {
  name         = "r"
  handoff      = "daily"
  length_hours = 8
}`,
				ExpectError: regexp.MustCompile(`length_hours is for a custom handoff`),
			},
			{
				Config: p + `
resource "tissue_mast_rotation" "r" { name = "r" }
resource "tissue_mast_override" "o" {
  rotation_id = tissue_mast_rotation.r.id
  user_id     = "usr_ana"
  start       = "2026-12-24T18:00"
  end         = "2026-12-24T09:00"
}`,
				ExpectError: regexp.MustCompile(`ends before it starts`),
			},
		},
	})
}

func TestAccPolicy(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	base := p + `
resource "tissue_mast_rotation" "primary" {
  name  = "primary"
  order = ["usr_ana", "usr_ben"]
}

resource "tissue_mast_channel" "ops" {
  name = "ops"
}
`
	policy := func(repeat string) string {
		return fmt.Sprintf(`
resource "tissue_mast_policy" "default" {
  name = "default"
  %s

  steps = [
    { target = "oncall", rotation_id = tissue_mast_rotation.primary.id, priority = "page" },
    { target = "next", rotation_id = tissue_mast_rotation.primary.id, priority = "page", after_secs = 300 },
    { target = "user", user_id = "usr_cy", priority = "loud", after_secs = 600 },
    { target = "channel", channel_id = tissue_mast_channel.ops.id, priority = "quiet", after_secs = 60 },
    { target = "everyone", priority = "page", after_secs = 900 },
  ]
}

resource "tissue_mast_channel" "api" {
  name      = "api"
  policy_id = tissue_mast_policy.default.id
}
`, repeat)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: base + policy(`repeat_every_secs = 1800`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_policy.default", "repeat_count", "3"),
					resource.TestCheckResourceAttr("tissue_mast_policy.default", "steps.0.after_secs", "0"),
					resource.TestCheckNoResourceAttr("tissue_mast_policy.default", "steps.4.rotation_id"),
					resource.TestCheckResourceAttrPair("tissue_mast_policy.default", "steps.3.channel_id", "tissue_mast_channel.ops", "id"),
					resource.TestCheckResourceAttrPair("tissue_mast_channel.api", "policy_id", "tissue_mast_policy.default", "id"),
				),
			},
			{
				Config: base + policy(`repeat_every_secs = 1800
  repeat_count = 5`),
				Check: resource.TestCheckResourceAttr("tissue_mast_policy.default", "repeat_count", "5"),
			},
			{
				// Without a count the server keeps the stored one.
				Config:           base + policy(`repeat_every_secs = 1800`),
				ConfigPlanChecks: noChange("tissue_mast_policy.default"),
			},
			{
				Config: base + policy(``),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("tissue_mast_policy.default", "repeat_every_secs"),
					resource.TestCheckNoResourceAttr("tissue_mast_policy.default", "repeat_count"),
				),
			},
			{
				ResourceName:      "tissue_mast_policy.default",
				ImportState:       true,
				ImportStateId:     "default",
				ImportStateVerify: true,
			},
			{
				// Someone moves the channel to another policy in the app.
				PreConfig: func() {
					f.app("api", func(ch *fakeChannel) { ch.policy = "" })
				},
				Config: base + policy(``),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tissue_mast_channel.api", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttrPair("tissue_mast_channel.api", "policy_id", "tissue_mast_policy.default", "id"),
			},
			{
				Config: base + policy(``) + `
data "tissue_mast_channel" "api" {
  name       = "api"
  depends_on = [tissue_mast_channel.api]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.tissue_mast_channel.api", "id", "tissue_mast_channel.api", "id"),
					resource.TestCheckResourceAttrPair("data.tissue_mast_channel.api", "url", "tissue_mast_channel.api", "url"),
					resource.TestCheckResourceAttrPair("data.tissue_mast_channel.api", "policy_id", "tissue_mast_policy.default", "id"),
					resource.TestCheckResourceAttr("data.tissue_mast_channel.api", "kind", "channel"),
					resource.TestCheckResourceAttr("data.tissue_mast_channel.api", "heartbeat", "false"),
				),
			},
		},
	})
}

func TestAccPolicyRules(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	cases := map[string]string{
		`steps = [{ target = "user", priority = "page" }]`:                             `needs`,
		`steps = [{ target = "everyone", user_id = "usr_ana", priority = "page" }]`:    `everyone`,
		`steps = [{ target = "channel", channel_id = "ops", priority = "page" }]`:      `must be an alert source id`,
		"repeat_count = 2\n  steps = [{ target = \"everyone\", priority = \"page\" }]": `repeat_every_secs`,
		`steps = [{ target = "oncall", rotation_id = "primary", priority = "page" }]`:  `must be a rotation id`,
	}
	var steps []resource.TestStep
	for body, want := range cases {
		steps = append(steps, resource.TestStep{
			Config:      p + "resource \"tissue_mast_policy\" \"p\" {\n  name = \"p\"\n  " + body + "\n}",
			ExpectError: regexp.MustCompile(want),
		})
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: steps})
}

func TestAccPeople(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
data "tissue_mast_people" "all" {}
data "tissue_mast_person" "ben" { email = "BEN@example.com" }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.tissue_mast_people.all", "people.#", "3"),
					resource.TestCheckResourceAttr("data.tissue_mast_person.ben", "user_id", "usr_ben"),
					resource.TestCheckResourceAttr("data.tissue_mast_person.ben", "role", "developer"),
				),
			},
			{
				Config:      p + `data "tissue_mast_person" "nobody" { email = "nobody@example.com" }`,
				ExpectError: regexp.MustCompile(`Invite\s+them first`),
			},
		},
	})
}

// A token that comes from another resource is unknown while planning, and
// Terraform still refreshes what is in state with the unconfigured provider.
func TestAccProviderConfigUnknownAtPlan(t *testing.T) {
	f := newFakeMast(t)
	known := providerBlock(f, fullToken)
	unknown := fmt.Sprintf("provider \"tissue\" {\n  server = %q\n  token  = \"%s${substr(timestamp(), 0, 0)}\"\n}\n",
		f.srv.URL, fullToken)
	channel := `resource "tissue_mast_channel" "x" { name = "alerts" }` + "\n"
	people := `data "tissue_mast_people" "all" {}` + "\n"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: known + channel},
			{
				Config:      unknown + channel,
				ExpectError: regexp.MustCompile(`provider isn't\s+configured yet`),
			},
			{
				Config:      unknown + channel + people,
				ExpectError: regexp.MustCompile(`provider isn't\s+configured yet`),
			},
			{Config: known + channel},
		},
	})
}

func TestAccChannelArchivedInTheApp(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	addr := "tissue_mast_channel.x"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `resource "tissue_mast_channel" "x" { name = "alerts" }`,
				Check:  resource.TestCheckResourceAttr(addr, "archived", "false"),
			},
			{
				// Archived from the app: the refresh shows it, and with the
				// argument left out Terraform leaves it archived.
				PreConfig:        func() { f.app("alerts", func(ch *fakeChannel) { ch.archived = true }) },
				Config:           p + `resource "tissue_mast_channel" "x" { name = "alerts" }`,
				ConfigPlanChecks: noChange(addr),
				Check:            resource.TestCheckResourceAttr(addr, "archived", "true"),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "x" {
  name     = "alerts"
  archived = false
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "archived", "false"),
					resource.TestMatchResourceAttr(addr, "url", regexp.MustCompile(`^https://mast\.tissue\.dev/mk_`)),
					func(*terraform.State) error {
						if f.resolveChannel("alerts").archived {
							return fmt.Errorf("still archived on the server")
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccChannelQuietZone(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_channel" "x" {
  name     = "alerts"
  quiet_tz = "Europe/Berln"
}`,
				ExpectError: regexp.MustCompile(`not a time zone`),
			},
			{
				Config: p + `
resource "tissue_mast_channel" "x" {
  name        = "alerts"
  quiet_start = 1320
  quiet_end   = 420
  quiet_tz    = "+0530"
}`,
				Check: resource.TestCheckResourceAttr("tissue_mast_channel.x", "quiet_tz", "+0530"),
			},
		},
	})
}

// The server caps names at 80 characters, not 80 bytes.
func TestAccNamesCountCharacters(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)
	name := strings.Repeat("ö", 80)
	config := func(name string) string {
		return p + fmt.Sprintf(`
resource "tissue_mast_rotation" "r" { name = %[1]q }

resource "tissue_mast_policy" "p" {
  name  = %[1]q
  steps = [{ target = "oncall", rotation_id = tissue_mast_rotation.r.id, priority = "page" }]
}`, name)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config:      config(name + "x"),
				ExpectError: regexp.MustCompile(`Invalid Attribute Value Length`),
			},
			{
				Config: config(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_rotation.r", "name", name),
					resource.TestCheckResourceAttr("tissue_mast_policy.p", "name", name),
				),
			},
		},
	})
}

// A rotation edit and an override added to the same rotation in one apply
// both write the whole schedule row. The override names the rotation through
// a data source, so Terraform doesn't order the two.
func TestAccRotationEditKeepsNewOverride(t *testing.T) {
	f := newFakeMast(t)
	f.schedulePatchGap = 200 * time.Millisecond
	p := providerBlock(f, fullToken)
	rotation := func(handoff string) string {
		return fmt.Sprintf(`
resource "tissue_mast_rotation" "primary" {
  name         = "primary"
  handoff_time = %q
  order        = ["usr_ana"]
}
`, handoff)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: p + rotation("09:00")},
			{
				Config: p + rotation("10:00") + `
data "tissue_mast_rotation" "primary" { name = "primary" }

resource "tissue_mast_override" "cover" {
  rotation_id = data.tissue_mast_rotation.primary.id
  user_id     = "usr_ben"
  start       = "2026-12-24"
  end         = "2026-12-26"
}`,
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, sc := range f.schedules {
						if sc.HandoffTime != "10:00" || len(sc.Overrides) != 1 {
							return fmt.Errorf("server has handoff %s and %d overrides, want 10:00 and 1",
								sc.HandoffTime, len(sc.Overrides))
						}
					}
					return nil
				},
			},
		},
	})
}

// A rotation or policy deleted in the app is planned back in, and so is an
// override whose rotation went with it.
func TestAccRotationAndPolicyGoneAreRecreated(t *testing.T) {
	f := newFakeMast(t)
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_rotation" "primary" {
  name  = "primary"
  order = ["usr_ana", "usr_ben"]
}

resource "tissue_mast_override" "holiday" {
  rotation_id = tissue_mast_rotation.primary.id
  user_id     = "usr_ben"
  start       = "2026-12-24"
  end         = "2026-12-26"
}

resource "tissue_mast_policy" "default" {
  name  = "default"
  steps = [{ target = "oncall", rotation_id = tissue_mast_rotation.primary.id, priority = "page" }]
}
`
	var rotation, policy string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					rotation = s.RootModule().Resources["tissue_mast_rotation.primary"].Primary.ID
					policy = s.RootModule().Resources["tissue_mast_policy.default"].Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					f.mu.Lock()
					delete(f.schedules, rotation)
					delete(f.policies, policy)
					f.mu.Unlock()
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("tissue_mast_override.holiday", "rotation_id", "tissue_mast_rotation.primary", "id"),
					resource.TestCheckResourceAttrPair("tissue_mast_policy.default", "steps.0.rotation_id", "tissue_mast_rotation.primary", "id"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources["tissue_mast_rotation.primary"].Primary.ID; id == rotation {
							return fmt.Errorf("rotation still %s", id)
						}
						if id := s.RootModule().Resources["tissue_mast_policy.default"].Primary.ID; id == policy {
							return fmt.Errorf("policy still %s", id)
						}
						f.mu.Lock()
						defer f.mu.Unlock()
						if len(f.schedules) != 1 || len(f.policies) != 1 {
							return fmt.Errorf("%d rotations and %d policies, want 1 each", len(f.schedules), len(f.policies))
						}
						for _, sc := range f.schedules {
							if len(sc.Overrides) != 1 {
								return fmt.Errorf("the rotation has %d overrides, want 1", len(sc.Overrides))
							}
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccImportErrors(t *testing.T) {
	f := newFakeMast(t)
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_heartbeat" "backup" {
  name       = "backup"
  period_secs = 86400
}

resource "tissue_mast_channel" "c" { name = "c" }

resource "tissue_mast_rotation" "a" {
  name  = "twin"
  order = ["usr_ana"]
}

resource "tissue_mast_rotation" "b" {
  name  = "twin"
  order = ["usr_ben"]
}

resource "tissue_mast_override" "o" {
  rotation_id = tissue_mast_rotation.a.id
  user_id     = "usr_ben"
  start       = "2026-12-24"
  end         = "2026-12-26"
}

resource "tissue_mast_policy" "a" {
  name  = "twin"
  steps = [{ target = "everyone", priority = "page" }]
}

resource "tissue_mast_policy" "b" {
  name  = "twin"
  steps = [{ target = "everyone", priority = "page" }]
}
`
	importing := func(addr, id, want string) resource.TestStep {
		return resource.TestStep{
			Config:        config,
			ResourceName:  addr,
			ImportState:   true,
			ImportStateId: id,
			ExpectError:   regexp.MustCompile(want),
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config},
			importing("tissue_mast_channel.c", "backup", `is a\s+heartbeat\.\s+Import\s+it\s+as\s+tissue_mast_heartbeat`),
			importing("tissue_mast_channel.c", "nope", `Finding the alert source failed`),
			importing("tissue_mast_heartbeat.backup", "c", `is an\s+alert\s+source\.\s+Import\s+it\s+as\s+tissue_mast_alert_source`),
			importing("tissue_mast_rotation.a", "nope", `no rotation named "nope"`),
			importing("tissue_mast_rotation.a", "twin", `2 rotations are named "twin"`),
			importing("tissue_mast_policy.a", "twin", `2 policies are named "twin"`),
			importing("tissue_mast_policy.a", "nope", `0 policies are named "nope"`),
			importing("tissue_mast_override.o", "ov_123", `Bad import id`),
			importing("tissue_mast_override.o", "twin/", `Bad import id`),
			importing("tissue_mast_override.o", "twin/ov_123", `2 rotations are named`),
			{
				Config:       config,
				ResourceName: "tissue_mast_override.o",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["tissue_mast_rotation.a"].Primary.ID + "/ov_missing", nil
				},
				ExpectError: regexp.MustCompile(`has no override ov_missing`),
			},
		},
	})
}

func TestAccProbe(t *testing.T) {
	f := newFakeMast(t)
	const addr = "tissue_mast_probe.shop"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  target = "https://shop.acme.test/health"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`^pr_`)),
					resource.TestCheckResourceAttr(addr, "name", "shop.acme.test"),
					resource.TestCheckResourceAttr(addr, "kind", "http"),
					resource.TestCheckResourceAttr(addr, "method", "GET"),
					resource.TestCheckResourceAttr(addr, "interval_secs", "300"),
					resource.TestCheckResourceAttr(addr, "expect", "up"),
					resource.TestCheckResourceAttr(addr, "ignore_case", "false"),
					resource.TestCheckResourceAttr(addr, "paused", "false"),
					resource.TestCheckResourceAttr(addr, "domain_expiry", "true"),
					resource.TestCheckNoResourceAttr(addr, "expect_status"),
					resource.TestCheckNoResourceAttr(addr, "request_content_type"),
					resource.TestCheckNoResourceAttr(addr, "auth"),
				),
			},
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  target = "https://shop.acme.test/health"
}
`,
				ConfigPlanChecks: noChange(addr),
			},
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_channel" "ops" {
  name = "ops"
}

resource "tissue_mast_policy" "default" {
  name  = "default"
  steps = [{ target = "everyone", priority = "page" }]
}

resource "tissue_mast_probe" "shop" {
  name            = "shop checkout"
  target          = "https://shop.acme.test/api/cart"
  method          = "POST"
  interval_secs   = 60
  expect_statuses = "200,201"
  expect_header   = "Content-Type: json"
  json_path       = "status"
  expect_value    = "ok"
  body_contains   = "ok"
  ignore_case     = true
  request_headers = ["Accept: application/json", "X-Probe: mast"]
  request_body    = "{\"sku\":\"test\"}"
  max_ms          = 2000
  host            = "web1"
  channel_id      = tissue_mast_channel.ops.id
  policy_id       = tissue_mast_policy.default.id
  domain_expiry   = false
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "shop checkout"),
					resource.TestCheckResourceAttr(addr, "method", "POST"),
					resource.TestCheckResourceAttr(addr, "interval_secs", "60"),
					resource.TestCheckResourceAttr(addr, "request_headers.#", "2"),
					resource.TestCheckResourceAttr(addr, "request_content_type", "application/json"),
					resource.TestCheckResourceAttr(addr, "max_ms", "2000"),
					resource.TestCheckResourceAttr(addr, "domain_expiry", "false"),
					resource.TestCheckResourceAttrPair(addr, "channel_id", "tissue_mast_channel.ops", "id"),
					resource.TestCheckResourceAttrPair(addr, "policy_id", "tissue_mast_policy.default", "id"),
				),
			},
			{ResourceName: addr, ImportState: true, ImportStateIdFunc: stateID(addr), ImportStateVerify: true},
			{ResourceName: addr, ImportState: true, ImportStateId: "shop checkout", ImportStateVerify: true},
			// Taking the conditions out of the configuration takes them off
			// the probe; the name and the interval stay as they are.
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  target = "https://shop.acme.test/api/cart"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "shop checkout"),
					resource.TestCheckResourceAttr(addr, "interval_secs", "60"),
					resource.TestCheckResourceAttr(addr, "method", "GET"),
					resource.TestCheckResourceAttr(addr, "ignore_case", "false"),
					resource.TestCheckResourceAttr(addr, "domain_expiry", "false"),
					resource.TestCheckNoResourceAttr(addr, "expect_statuses"),
					resource.TestCheckNoResourceAttr(addr, "json_path"),
					resource.TestCheckNoResourceAttr(addr, "body_contains"),
					resource.TestCheckNoResourceAttr(addr, "request_headers.#"),
					resource.TestCheckNoResourceAttr(addr, "request_body"),
					resource.TestCheckNoResourceAttr(addr, "request_content_type"),
					resource.TestCheckNoResourceAttr(addr, "max_ms"),
					resource.TestCheckNoResourceAttr(addr, "host"),
					resource.TestCheckNoResourceAttr(addr, "channel_id"),
					resource.TestCheckNoResourceAttr(addr, "policy_id"),
				),
			},
		},
	})
}

func TestAccProbeKinds(t *testing.T) {
	f := newFakeMast(t)
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "dns" {
  kind       = "dns"
  target     = "acme.test"
  method     = "MX"
  expect_set = "mx1.acme.test,mx2.acme.test"
  resolver   = "9.9.9.9"
}

resource "tissue_mast_probe" "redis" {
  kind          = "tcp"
  target        = "cache.acme.test:6379"
  send          = "PING\\r\\n"
  body_contains = "PONG"
}

resource "tissue_mast_probe" "cert" {
  kind   = "tls"
  target = "acme.test:443"
}

resource "tissue_mast_probe" "mail" {
  kind   = "smtp"
  target = "mx1.acme.test:25"
  method = "STARTTLS"
}

resource "tissue_mast_probe" "router" {
  kind   = "icmp"
  target = "192.0.2.10"
}

# The admin port has to stay closed to the outside.
resource "tissue_mast_probe" "admin" {
  kind   = "tcp"
  target = "db.acme.test:5432"
  expect = "down"
  paused = true
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_probe.dns", "method", "MX"),
					resource.TestCheckResourceAttr("tissue_mast_probe.redis", "method", ""),
					resource.TestCheckResourceAttr("tissue_mast_probe.redis", "send", `PING\r\n`),
					resource.TestCheckResourceAttr("tissue_mast_probe.cert", "method", ""),
					resource.TestCheckResourceAttr("tissue_mast_probe.mail", "method", "STARTTLS"),
					resource.TestCheckResourceAttr("tissue_mast_probe.router", "domain_expiry", "false"),
					resource.TestCheckResourceAttr("tissue_mast_probe.admin", "expect", "down"),
					resource.TestCheckResourceAttr("tissue_mast_probe.admin", "paused", "true"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			// A dns probe without a record type asks for A.
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "dns" {
  kind   = "dns"
  target = "acme.test"
}
`,
				Check: resource.TestCheckResourceAttr("tissue_mast_probe.dns", "method", "A"),
			},
			// Another kind is another probe.
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "dns" {
  kind   = "icmp"
  target = "acme.test"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("tissue_mast_probe.dns", plancheck.ResourceActionReplace),
				}},
			},
		},
	})
}

func TestAccProbeAuth(t *testing.T) {
	f := newFakeMast(t)
	const addr = "tissue_mast_probe.api"
	config := func(target, auth string) string {
		return providerBlock(f, fullToken) + fmt.Sprintf(`
resource "tissue_mast_probe" "api" {
  target = %q
  %s
}
`, target, auth)
	}
	secret := func(want string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			pr := f.probes[s.RootModule().Resources[addr].Primary.ID]
			if pr.AuthSecret != want {
				return fmt.Errorf("the probe holds the secret %q, want %q", pr.AuthSecret, want)
			}
			return nil
		}
	}
	basic := `auth = { type = "basic", username = "probe", password = "hunter2" }`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("https://api.acme.test/private", basic),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "auth.type", "basic"),
					resource.TestCheckResourceAttr(addr, "auth.username", "probe"),
					resource.TestCheckResourceAttr(addr, "auth.password", "hunter2"),
					secret("hunter2"),
				),
			},
			{Config: config("https://api.acme.test/private", basic), ConfigPlanChecks: noChange(addr)},
			{
				ResourceName: addr, ImportState: true, ImportStateIdFunc: stateID(addr), ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"auth.password"},
			},
			// The server keeps credentials off a host they weren't given for,
			// so a new target sends them again.
			{
				Config: config("https://api2.acme.test/private", basic),
				Check:  secret("hunter2"),
			},
			{
				Config: config("https://api2.acme.test/private", `auth = { type = "bearer", token = "tok-1" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "auth.type", "bearer"),
					resource.TestCheckNoResourceAttr(addr, "auth.username"),
					secret("tok-1"),
				),
			},
			{
				Config: config("https://api2.acme.test/private", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "auth.type"),
					secret(""),
				),
			},
		},
	})
}

func TestAccProbeDuplicate(t *testing.T) {
	f := newFakeMast(t)
	var first string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "first" {
  target = "https://shop.acme.test/health"
}

resource "tissue_mast_probe" "other" {
  target = "https://shop.acme.test/status"
}
`,
				Check: func(s *terraform.State) error {
					first = s.RootModule().Resources["tissue_mast_probe.first"].Primary.ID
					return nil
				},
			},
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "first" {
  target = "https://shop.acme.test/health"
}

resource "tissue_mast_probe" "other" {
  target = "https://shop.acme.test/status"
}

resource "tissue_mast_probe" "second" {
  name   = "the same check under another name"
  target = "https://shop.acme.test/health"
}
`,
				ExpectError: regexp.MustCompile(`(?s)A probe already checks this.*terraform import tissue_mast_probe\.<name> pr_[0-9a-f]+`),
			},
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "first" {
  target = "https://shop.acme.test/health"
}

resource "tissue_mast_probe" "other" {
  target = "https://shop.acme.test/health"
}
`,
				ExpectError: regexp.MustCompile(`(?s)A probe already checks this.*exactly\s+what\s+pr_[0-9a-f]+\s+checks`),
			},
			// The probe the refusal names is the one to import.
			{
				Config: providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "first" {
  target = "https://shop.acme.test/health"
}
`,
				Check: func(s *terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if _, ok := f.probes[first]; !ok || len(f.probes) != 1 {
						return fmt.Errorf("want only %s left, have %d probes", first, len(f.probes))
					}
					return nil
				},
			},
		},
	})
}

func TestAccProbeRules(t *testing.T) {
	f := newFakeMast(t)
	probe := func(body string) string {
		return providerBlock(f, fullToken) + "\nresource \"tissue_mast_probe\" \"p\" {\n" + body + "\n}\n"
	}
	cases := []struct{ body, want string }{
		{`kind = "tcp"
  target = "db.acme.test:5432"
  expect_status = 200`, `expect_status applies to http probes, and this one is tcp`},
		{`target = "https://acme.test"
  resolver = "9.9.9.9"`, `resolver applies to dns probes, and this one is http`},
		{`kind = "icmp"
  target = "acme.test"
  method = "GET"`, `method applies to http, dns, smtp probes`},
		{`target = "https://acme.test"
  method = "get"`, `one of GET, HEAD, POST, PUT, PATCH, in capitals`},
		{`kind = "dns"
  target = "acme.test"
  method = "SRV"`, `one of A, AAAA, CNAME, MX, TXT, NS`},
		{`target = "https://acme.test"
  request_body = "{}"`, `Set method to POST, PUT or PATCH`},
		{`target = "https://acme.test"
  request_content_type = "text/plain"`, `request_content_type needs request_body`},
		{`target = "https://acme.test"
  expect_value = "ok"`, `expect_value needs json_path`},
		{`target = "https://acme.test"
  method = "HEAD"
  body_contains = "ok"`, `A HEAD request has no body to check`},
		{`target = "https://acme.test"
  request_headers = ["Accept:application/json"]`, `stored as "Accept: application/json"`},
		{`target = "https://acme.test"
  request_headers = ["no colon here"]`, `is not a "Name: value" line`},
		{`target = "https://acme.test"
  auth = { type = "basic", username = "probe" }`, `Basic auth needs username and password`},
		{`target = "https://acme.test"
  auth = { type = "bearer", token = "t", username = "probe" }`, `Not used by bearer auth`},
		{`target = " https://acme.test"`, `target`},
		{`target = "https://acme.test"
  interval_secs = 10`, `interval_secs`},
		{`target = "https://acme.test"
  policy_id = "default"`, `must be a policy id, po_`},
	}
	var steps []resource.TestStep
	for _, c := range cases {
		steps = append(steps, resource.TestStep{
			Config:      probe(c.body),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(strings.ReplaceAll(regexp.QuoteMeta(c.want), " ", `\s+`)),
		})
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: steps})

	if n := f.count("POST /v1/mast/probes"); n != 0 {
		t.Errorf("%d probes were made from configurations that should not pass", n)
	}
}

func TestAccProbeGoneIsRecreated(t *testing.T) {
	f := newFakeMast(t)
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  target = "https://shop.acme.test/health"
}
`
	var id string

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					id = s.RootModule().Resources["tissue_mast_probe.shop"].Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					f.mu.Lock()
					delete(f.probes, id)
					f.mu.Unlock()
				},
				Config: config,
				Check: func(s *terraform.State) error {
					if got := s.RootModule().Resources["tissue_mast_probe.shop"].Primary.ID; got == id {
						return fmt.Errorf("the probe kept the id of the one that was deleted")
					}
					return nil
				},
			},
		},
	})
}

func TestAccProbeChangedInTheApp(t *testing.T) {
	f := newFakeMast(t)
	const addr = "tissue_mast_probe.shop"
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  target        = "https://shop.acme.test/health"
  expect_status = 200
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config},
			// Pausing and the interval are left to the app; a condition
			// added there is not in the configuration, so it goes.
			{
				PreConfig: func() {
					f.mu.Lock()
					for _, pr := range f.probes {
						pr.Paused, pr.IntervalSecs = true, 60
						pr.Text["body_contains"] = "ok"
					}
					f.mu.Unlock()
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "paused", "true"),
					resource.TestCheckResourceAttr(addr, "interval_secs", "60"),
					resource.TestCheckNoResourceAttr(addr, "body_contains"),
				),
			},
		},
	})
}

func TestAccProbeDataSource(t *testing.T) {
	f := newFakeMast(t)
	config := providerBlock(f, fullToken) + `
resource "tissue_mast_probe" "shop" {
  name   = "shop"
  target = "https://shop.acme.test/health"
  host   = "web1"
}

data "tissue_mast_probe" "by_name" {
  name = tissue_mast_probe.shop.name
}

data "tissue_mast_probe" "by_id" {
  id = tissue_mast_probe.shop.id
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.tissue_mast_probe.by_name", "id", "tissue_mast_probe.shop", "id"),
					resource.TestCheckResourceAttr("data.tissue_mast_probe.by_name", "target", "https://shop.acme.test/health"),
					resource.TestCheckResourceAttr("data.tissue_mast_probe.by_name", "state", "pending"),
					resource.TestCheckResourceAttr("data.tissue_mast_probe.by_id", "name", "shop"),
					resource.TestCheckResourceAttr("data.tissue_mast_probe.by_id", "host", "web1"),
					resource.TestCheckNoResourceAttr("data.tissue_mast_probe.by_id", "channel_id"),
				),
			},
			{
				Config: providerBlock(f, fullToken) + `
data "tissue_mast_probe" "missing" {
  name = "nothing by this name"
}
`,
				ExpectError: regexp.MustCompile(`no probe named "nothing by this name"`),
			},
		},
	})
}

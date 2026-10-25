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

func TestAccVital(t *testing.T) {
	f := newFakeMast(t)
	p := providerBlock(f, fullToken)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "tissue_mast_vital" "backup" {
  name     = "nightly-backup"
  schedule = "0 3 * * *"
}`,
				ExpectError: regexp.MustCompile(`A schedule needs grace_secs`),
			},
			{
				Config: p + `
resource "tissue_mast_vital" "backup" {
  name           = "nightly-backup"
  schedule       = "0 3 * * *"
  schedule_tz    = "America/Los_Angeles"
  grace_secs     = 1800
  host           = "db1"
  vital_priority = "page"
  paused         = true
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "paused", "true"),
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "period_secs", "0"),
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "fail_threshold", "1"),
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "smart_early_warn", "false"),
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "max_priority", "loud"),
					resource.TestCheckResourceAttrSet("tissue_mast_vital.backup", "url"),
					func(*terraform.State) error {
						if ch := f.resolveChannel("nightly-backup"); ch.Kind != "vital" || ch.VitalState != "paused" {
							return fmt.Errorf("server has kind %s, state %s", ch.Kind, ch.VitalState)
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
resource "tissue_mast_vital" "backup" {
  name           = "nightly-backup"
  schedule       = "0 3 * * *"
  schedule_tz    = "America/Los_Angeles"
  grace_secs     = 1800
  host           = "db1"
  vital_priority = "page"
}`,
				ConfigPlanChecks: noChange("tissue_mast_vital.backup"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "paused", "false"),
					resource.TestCheckResourceAttr("tissue_mast_vital.backup", "smart_early_warn", "true"),
				),
			},
			{
				ResourceName:      "tissue_mast_vital.backup",
				ImportState:       true,
				ImportStateId:     "nightly-backup",
				ImportStateVerify: true,
			},
			{
				Config: p + `
resource "tissue_mast_vital" "backup" {
  name           = "nightly-backup"
  schedule       = "0 3 * * *"
  schedule_tz    = "America/Los_Angeles"
  grace_secs     = 1800
  host           = "db1"
  vital_priority = "page"
}

resource "tissue_mast_channel" "wrong" {
  name = "nightly-backup"
}`,
				ResourceName:  "tissue_mast_channel.wrong",
				ImportState:   true,
				ImportStateId: "nightly-backup",
				ExpectError:   regexp.MustCompile(`Import it as\s+tissue_mast_vital`),
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
		`steps = [{ target = "channel", channel_id = "ops", priority = "page" }]`:      `must be a channel id`,
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

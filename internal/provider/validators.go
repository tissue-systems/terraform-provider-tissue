package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // zoneProblem must know the same zones wherever Terraform runs
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"golang.org/x/text/unicode/norm"
)

const channelNameMax = 32

// channelNameProblem mirrors the server's name rule, so a name it would
// refuse, or store spelled differently, fails at plan time instead.
func channelNameProblem(name string) string {
	if name == "" {
		return "a channel name can't be empty"
	}
	if !norm.NFC.IsNormalString(name) {
		return "a channel name must be in composed (NFC) form; the server stores it that way"
	}
	first, _ := utf8.DecodeRuneInString(name)
	if first == ' ' {
		return "a channel name can't start with a space"
	}
	afterSpace := false
	for i, r := range name {
		if r == ' ' {
			if afterSpace {
				return "a channel name can't have two spaces in a row"
			}
			afterSpace = true
			continue
		}
		afterSpace = false
		if unicode.ToLower(r) != r || unicode.IsTitle(r) {
			return fmt.Sprintf("channel names are lowercase (%q)", r)
		}
		if i == 0 && !letterOrDigit(r) {
			return fmt.Sprintf("a channel name starts with a letter or a digit, not %q", r)
		}
		if !letterOrDigit(r) && r != '.' && r != '_' && r != '-' {
			return fmt.Sprintf("%q can't be used in a channel name; letters, digits, '.', '_', '-' and single spaces can", r)
		}
	}
	if strings.HasSuffix(name, " ") {
		return "a channel name can't end with a space"
	}
	if n := utf8.RuneCountInString(name); n > channelNameMax {
		return fmt.Sprintf("a channel name is at most %d characters, and this one is %d", channelNameMax, n)
	}
	return ""
}

func letterOrDigit(r rune) bool {
	switch r {
	case 'ᅟ', 'ᅠ', '឴', '឵', 'ㅤ', 'ﾠ':
		return false
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

type channelNameValidator struct{}

func (channelNameValidator) Description(context.Context) string {
	return "lowercase letters, digits, '.', '_', '-' and single spaces, at most 32 characters"
}

func (v channelNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (channelNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if p := channelNameProblem(req.ConfigValue.ValueString()); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid channel name", p+".")
	}
}

// trimmedValidator refuses surrounding whitespace on fields the server trims,
// which would otherwise come back different from the config and fail the apply.
type trimmedValidator struct{}

func (trimmedValidator) Description(context.Context) string {
	return "no leading or trailing whitespace"
}

func (v trimmedValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (trimmedValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	v := req.ConfigValue.ValueString()
	if strings.TrimSpace(v) != v {
		resp.Diagnostics.AddAttributeError(req.Path, "Surrounding whitespace",
			"The server trims this field, so it would never match the configuration. Remove the spaces.")
	}
}

// Override bounds are local to the rotation's time zone: a date, which as a
// start means 00:00 and as an end means the end of that day, or a time.
func overrideStart(s string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if len(s) == 16 {
		if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func overrideEnd(s string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.AddDate(0, 0, 1), true
	}
	return overrideStart(s)
}

type overrideBoundValidator struct{}

func (overrideBoundValidator) Description(context.Context) string {
	return "a date, YYYY-MM-DD, or a local time, YYYY-MM-DDTHH:MM"
}

func (v overrideBoundValidator) MarkdownDescription(ctx context.Context) string {
	return "a date, `YYYY-MM-DD`, or a local time, `YYYY-MM-DDTHH:MM`"
}

func (overrideBoundValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, ok := overrideStart(req.ConfigValue.ValueString()); !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid date or time",
			fmt.Sprintf("%q is neither YYYY-MM-DD nor YYYY-MM-DDTHH:MM.", req.ConfigValue.ValueString()))
	}
}

// zoneProblem is the server's validate_zone, which it applies to a vital's
// schedule_tz. quiet_tz is stored without a check, and delivery reads a zone
// it doesn't know as UTC, so a typo there would move the quiet window without
// a word.
func zoneProblem(tz string) string {
	if tz == "" || strings.EqualFold(tz, "utc") || tz == "Z" {
		return ""
	}
	if len(tz) > 64 {
		return "is longer than 64 characters"
	}
	if tz[0] == '+' || tz[0] == '-' {
		digits := strings.ReplaceAll(tz[1:], ":", "")
		if len(digits) != 4 || strings.Trim(digits, "0123456789") != "" {
			return "is not an offset: use +HHMM or +HH:MM, such as +0530"
		}
		if digits[:2] > "14" || digits[2:] > "59" {
			return "is not an offset: hours run to 14 and minutes to 59"
		}
		return ""
	}
	if strings.Trim(tz, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/_-+") != "" {
		return "is not a time zone: IANA names use letters, digits, / _ - and +"
	}
	// LoadLocation takes "Local" to mean the machine running Terraform.
	if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		return "is not a time zone Mast knows, such as Europe/Berlin or America/New_York"
	}
	return ""
}

type zoneValidator struct{}

func (zoneValidator) Description(context.Context) string {
	return "an IANA time zone, a +HHMM offset, or empty for UTC"
}

func (v zoneValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (zoneValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	tz := req.ConfigValue.ValueString()
	if p := zoneProblem(tz); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid time zone", fmt.Sprintf("%q %s.", tz, p))
	}
}

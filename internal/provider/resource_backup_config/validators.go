package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// noTrailingSlashValidator rejects a string value ending in "/". This exists
// for backup_location: it is Required (non-Computed), but Read() trims a
// trailing slash from the API response (strings.TrimRight). A non-Computed
// attribute's post-apply state must exactly equal its planned (== configured)
// value, so a plan modifier cannot be used to silently normalize this
// instead — the terraform-plugin-framework rejects that as "Provider
// produced invalid plan". Failing fast at plan/validate time with a clear
// message is both correct and more helpful than either outcome.
type noTrailingSlashValidator struct{}

func noTrailingSlash() validator.String {
	return noTrailingSlashValidator{}
}

func (v noTrailingSlashValidator) Description(_ context.Context) string {
	return "value must not end in a trailing slash"
}

func (v noTrailingSlashValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v noTrailingSlashValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	value := req.ConfigValue.ValueString()
	if strings.HasSuffix(value, "/") {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Trailing Slash Not Allowed",
			fmt.Sprintf("%q must not end in a trailing slash (the API strips it, which would otherwise make "+
				"every plan after this one show a permanent diff); use %q instead.",
				value, strings.TrimRight(value, "/")),
		)
	}
}

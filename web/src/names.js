// Subdomain shape rule — docs/demos.md §Reserved subdomains (2–32 chars,
// lowercase letters/digits/hyphens, no leading/trailing hyphen). The server
// is the authority (shape + reserved list, 409); this is client-side
// pre-validation only, so the copy matches the backend's rule.
export const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/

export const NAME_HINT =
  'Lowercase letters, digits and hyphens (up to 32 characters); no leading or trailing hyphen.'

export const isValidDemoName = (name) => NAME_PATTERN.test(name)

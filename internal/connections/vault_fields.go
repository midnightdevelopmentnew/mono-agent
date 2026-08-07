package connections

import "fmt"

// secretFieldKeys returns which Data keys under platform/method are
// credential-bearing.
func secretFieldKeys(platform string, method AuthMethod) map[string]bool {
	keys := map[string]bool{}
	if method == MethodOAuth {
		keys["access_token"] = true
		keys["refresh_token"] = true
		return keys
	}
	def, ok := Get(platform)
	if !ok {
		return keys
	}
	for _, f := range def.Fields[method] {
		if f.Secret {
			keys[f.Key] = true
		}
	}
	return keys
}

// splitSecretFields partitions a connection's Data map into the
// credential-bearing fields and everything else, using secretFieldKeys' key
// set for platform/method. The first return value is string-typed only,
// matching what the vault's field storage accepts — every value this
// codebase's connectors write under a credential-bearing key is a string in
// practice (token strings, API key strings), so a non-string or
// empty-string value under such a key is simply dropped rather than
// coerced, since there would be nothing meaningful to persist for it.
func splitSecretFields(platform string, method AuthMethod, data map[string]interface{}) (map[string]string, map[string]interface{}) {
	keys := secretFieldKeys(platform, method)
	secretFields := make(map[string]string)
	nonSecret := make(map[string]interface{})
	for k, v := range data {
		if !keys[k] {
			nonSecret[k] = v
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			secretFields[k] = s
		}
	}
	return secretFields, nonSecret
}

// connectionVaultName builds the display name for c's linked vault entry:
// "{platform display name} — {label or account id}", or just the platform
// name if neither is set. Connections support multiple accounts per
// platform (Label/AccountID), so this must disambiguate the same way the
// Connections page already does.
func connectionVaultName(c *Connection) string {
	label := c.Label
	if label == "" {
		label = c.AccountID
	}
	name := c.Platform
	if def, ok := Get(c.Platform); ok {
		name = def.Name
	}
	if label == "" {
		return name
	}
	return fmt.Sprintf("%s — %s", name, label)
}

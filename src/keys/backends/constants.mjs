/**
 * Constants shared by the keychain backends. No imports on purpose: `security-cli.mjs` must stay
 * loadable when `@napi-rs/keyring` (and anything that touches it) cannot load.
 */

/** Keychain service name; the account is the key name. */
export const SERVICE = 'code-forge';

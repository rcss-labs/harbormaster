import type { SSOConfigDTO } from '../../../api/types'

/**
 * Body for `PUT /api/sso`. The endpoint saves every manual field at once, so unchanged fields are
 * echoed from the loaded configuration. `idp_certs` is left out unless given: omitting it keeps the stored
 * certificates, because the API never returns the PEM.
 */
export function configBody(config: SSOConfigDTO, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    idp_entity_id: config.idp_entity_id,
    idp_sso_redirect_url: config.idp_sso_redirect_url,
    name_id_format: config.name_id_format,
    attr_email: config.attr_email,
    attr_display_name: config.attr_display_name,
    attr_username: config.attr_username,
    clock_skew_seconds: config.clock_skew_seconds,
    button_label: config.button_label,
    ...overrides,
  }
}

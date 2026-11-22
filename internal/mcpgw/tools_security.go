package mcpgw

// securityTools read image vulnerability scan results.
func securityTools() []Tool {
	return []Tool{
		{
			// internal/api/security.go (listSecurity): scopeFrom.
			Name: "list_security", Title: "List image security",
			Description: "Vulnerability scan results for the container images running in the estate: per image its digest (pass it to " +
				"get_image_security), the services using it, finding counts by severity and scan time, plus the scanner's status. When scanning " +
				"is off the answer says so." + scopedNote,
			Route:  get("/api/security"),
			Params: scopeParams(),
		},
		{
			// internal/api/security.go (getSecurityService): the service id path value only.
			Name: "get_service_security", Title: "Get a service's image security",
			Description: "Vulnerability scan results for the images of one service's containers, with finding counts by severity and image digests " +
				"to pass to get_image_security.",
			Route:  get("/api/security/services/{serviceId}"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/security.go (getSecurityImage): the digest path value and scopeFrom.
			Name: "get_image_security", Title: "Get image vulnerabilities",
			Description: "One scanned image in detail: every finding with its vulnerability id, severity, package, installed and fixed versions, " +
				"and any accepted risk, plus the services that run the image.",
			Route: get("/api/security/images/{digest}"),
			Params: []Param{
				{Name: "digest", Type: TypeString, In: InPath, Required: true,
					Description: "The image digest (sha256:...), from list_security or get_service_security."},
			},
		},
	}
}

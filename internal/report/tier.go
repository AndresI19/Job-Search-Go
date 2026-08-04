package report

// company-tier classification, loaded once from the same embedded lists the xlsx
// colouring uses. Exposed so the typed api/results contract can carry a company's
// tier and the client can render the company-column colour coding.
var (
	tierF500     = loadCompanies(fortune500Raw)
	tierSoftware = loadCompanies(softwareRaw)
)

// CompanyTier classifies a company into the same tiers the xlsx/Preview colouring
// applies, in the same precedence order as fillFor: Fortune 500, then a high-pay
// software company, then a startup (headcount in (0, startupMax) and not a
// staffing/consulting body-shop). Returns "" when none apply. A startupMax <= 0
// disables the startup tag.
func CompanyTier(company string, size, startupMax int, industries string) string {
	switch {
	case tierF500.match(company):
		return "f500"
	case tierSoftware.match(company):
		return "software"
	case size > 0 && startupMax > 0 && size < startupMax && !isConsultingShop(industries):
		return "startup"
	default:
		return ""
	}
}

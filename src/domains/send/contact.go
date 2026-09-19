package send

type ContactRequest struct {
	BaseRequest
	ContactName  string `json:"contact_name" form:"contact_name"`
	ContactPhone string `json:"contact_phone" form:"contact_phone"`
	// VCard carries a complete caller-built vCard. When set it replaces the
	// vCard generated from ContactName/ContactPhone, which then only feed the
	// display name and the stored content preview.
	VCard string `json:"vcard,omitempty" form:"vcard"`
}

package compat

import (
	"fmt"
	"strconv"
	"strings"
)

// This file ports the legacy service's VCardGenerator (setContact + build)
// so contact cards sent through the compatibility API carry exactly the same
// vCard content: same field mapping, same Iranian phone formatting, same
// line order and CRLF framing. Behavior quirks are kept on purpose — e.g.
// formatDisplayNumber rejects non-Iranian main/mobile numbers, which the
// legacy controller turned into a failed send.

// getStr reads a contact field with the loose coercion of the legacy
// JavaScript: strings pass through, numbers render without JSON float noise,
// booleans render as true/false, anything else is empty.
func getStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	}
	return ""
}

// escapeVCardValue mirrors VCardGenerator._escapeValue: newlines, commas, and
// semicolons are backslash-escaped in that order.
func escapeVCardValue(val string) string {
	return strings.NewReplacer("\n", `\n`, ",", `\,`, ";", `\;`).Replace(val)
}

// normalizeDigits mirrors VCardGenerator._normalizeNumber: digits only.
func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isMobilePhone mirrors VCardGenerator.isMobilePhone: Iranian local mobile
// format (11 digits starting with 09).
func isMobilePhone(phone string) bool {
	return strings.HasPrefix(phone, "09") && len(phone) == 11
}

// convertToWhatsAppFormat mirrors VCardGenerator.convertToWhatsAppFormat:
// strip non-digits, turn a leading 09 into the 98 country code.
func convertToWhatsAppFormat(phone string) string {
	clean := normalizeDigits(phone)
	if strings.HasPrefix(clean, "09") {
		clean = "98" + clean[1:]
	}
	return clean
}

// formatDisplayNumber mirrors VCardGenerator.formatDisplayNumber, including
// its error on numbers that are not in one of the three Iranian mobile
// formats — the legacy send failed with that message, and so does this one.
func formatDisplayNumber(raw string) (string, error) {
	cleaned := normalizeDigits(raw)
	if len(cleaned) == 11 && strings.HasPrefix(cleaned, "09") {
		return fmt.Sprintf("+98 %s %s %s", cleaned[1:4], cleaned[4:7], cleaned[7:]), nil
	}
	if len(cleaned) == 12 && strings.HasPrefix(cleaned, "989") {
		return fmt.Sprintf("+98 %s %s %s", cleaned[2:5], cleaned[5:8], cleaned[8:]), nil
	}
	if len(cleaned) == 10 && strings.HasPrefix(cleaned, "9") {
		return fmt.Sprintf("+98 %s %s %s", cleaned[0:3], cleaned[3:6], cleaned[6:]), nil
	}
	return "", fmt.Errorf("Invalid Iranian mobile number format: '%s'. Must be 11-digit (09xx...), 12-digit (989xx...), or 10-digit (9xx...) format", raw)
}

type vcardPhone struct {
	paramsStr string
	display   string
}

type vcardAddress struct {
	poBox, ext, street, city, region, postalCode, country, typ string
}

type vcardBuilder struct {
	fn                                        string
	family, given, additional, prefix, suffix string
	nSet                                      bool
	phones                                    []vcardPhone
	org, orgUnit, title                       string
	address                                   *vcardAddress
	url, bday                                 string
}

func (b *vcardBuilder) addPhone(number string, types []string, display string) {
	raw := strings.TrimSpace(number)
	if raw == "" {
		return
	}
	waid := normalizeDigits(raw)
	paramsStr := ""
	if len(types) > 0 {
		paramsStr = ";type=" + strings.Join(types, ";")
	}
	paramsStr += ";waid=" + waid
	disp := display
	if disp == "" {
		disp = raw
	}
	b.phones = append(b.phones, vcardPhone{paramsStr: paramsStr, display: disp})
}

// setContact ports VCardGenerator.setContact field by field.
func (b *vcardBuilder) setContact(c map[string]any) error {
	b.fn = strings.TrimSpace(getStr(c, "full_name"))
	b.family = escapeVCardValue(getStr(c, "last_name"))
	b.given = escapeVCardValue(getStr(c, "first_name"))
	b.additional = ""
	b.prefix = escapeVCardValue(getStr(c, "gender"))
	b.suffix = ""
	b.nSet = true
	if b.fn == "" {
		// setFormattedName('') leaves FN unset, so setName rebuilds it from the
		// raw (unescaped) name components, joined and trimmed.
		var parts []string
		for _, part := range []string{getStr(c, "gender"), getStr(c, "first_name"), "", getStr(c, "last_name"), ""} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		b.fn = strings.TrimSpace(strings.Join(parts, " "))
	}

	main := getStr(c, "main_phone_number")
	if main != "" {
		types := []string{"VOICE"}
		phone := main
		if isMobilePhone(main) {
			types = append(types, "CELL")
			phone = convertToWhatsAppFormat(main)
		}
		disp, err := formatDisplayNumber(main)
		if err != nil {
			return err
		}
		b.addPhone(phone, types, disp)
	}

	if m1 := getStr(c, "mobile_number_1"); m1 != "" && m1 != main {
		disp, err := formatDisplayNumber(m1)
		if err != nil {
			return err
		}
		b.addPhone(convertToWhatsAppFormat(m1), []string{"CELL", "VOICE"}, disp)
	}

	if m2 := getStr(c, "mobile_number_2"); m2 != "" {
		disp, err := formatDisplayNumber(m2)
		if err != nil {
			return err
		}
		b.addPhone(convertToWhatsAppFormat(m2), []string{"CELL", "VOICE"}, disp)
	}

	if home := getStr(c, "home_phone_number"); home != "" {
		b.addPhone(home, []string{"HOME", "VOICE"}, home)
	}

	if wa := getStr(c, "whatsapp"); wa != "" && wa != main {
		b.addPhone(convertToWhatsAppFormat(wa), []string{"CELL", "VOICE"}, wa)
	}

	if org := getStr(c, "company_name"); org != "" {
		b.org = org
	}
	if title := getStr(c, "job_position"); title != "" {
		b.title = title
	}
	city, province := getStr(c, "city"), getStr(c, "province")
	if city != "" || province != "" {
		b.address = &vcardAddress{
			street:     getStr(c, "address"),
			city:       city,
			region:     province,
			postalCode: getStr(c, "zip_code"),
			country:    "",
			typ:        "HOME",
		}
	}
	if bday := getStr(c, "birth_date"); bday != "" {
		b.bday = strings.ReplaceAll(bday, "-", "")
	}
	b.url = getStr(c, "website")
	return nil
}

// build ports VCardGenerator.build: same line order (N, FN, ORG, TITLE, TEL,
// ADR, URL, BDAY), CRLF-joined with a trailing CRLF. PHOTO/categories/custom
// fields had no source in the legacy request and are never emitted.
func (b *vcardBuilder) build() string {
	lines := []string{"BEGIN:VCARD", "VERSION:3.0"}

	if b.nSet {
		lines = append(lines, fmt.Sprintf("N:%s;%s;%s;%s;%s",
			b.family, b.given, b.additional, b.prefix, b.suffix))
	}
	if b.fn != "" {
		lines = append(lines, "FN:"+escapeVCardValue(b.fn))
	}
	if b.org != "" {
		orgLine := escapeVCardValue(b.org)
		if b.orgUnit != "" {
			orgLine += ";" + escapeVCardValue(b.orgUnit)
		}
		lines = append(lines, "ORG:"+orgLine)
	}
	if b.title != "" {
		lines = append(lines, "TITLE:"+escapeVCardValue(b.title))
	}
	for _, p := range b.phones {
		lines = append(lines, fmt.Sprintf("TEL%s:%s", p.paramsStr, escapeVCardValue(p.display)))
	}
	if b.address != nil {
		a := b.address
		lines = append(lines, fmt.Sprintf("ADR;type=%s:%s;%s;%s;%s;%s;%s;%s", a.typ,
			escapeVCardValue(a.poBox), escapeVCardValue(a.ext), escapeVCardValue(a.street),
			escapeVCardValue(a.city), escapeVCardValue(a.region),
			escapeVCardValue(a.postalCode), escapeVCardValue(a.country)))
	}
	if b.url != "" {
		lines = append(lines, "URL:"+escapeVCardValue(b.url))
	}
	if b.bday != "" {
		lines = append(lines, "BDAY:"+b.bday)
	}

	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n") + "\r\n"
}

// buildVCardForContact turns a legacy contact object into the pieces
// SendContact needs: the vCard text, the display name (FN, or "Contact" like
// toBaileysContact's fallback), and a representative phone for the stored
// content preview.
func buildVCardForContact(c map[string]any) (vcard, displayName, contactPhone string, err error) {
	b := &vcardBuilder{}
	if err = b.setContact(c); err != nil {
		return "", "", "", err
	}

	vcard = b.build()
	displayName = b.fn
	if displayName == "" {
		displayName = "Contact"
	}

	contactPhone = convertToWhatsAppFormat(getStr(c, "main_phone_number"))
	if contactPhone == "" {
		for _, key := range []string{"mobile_number_1", "mobile_number_2", "whatsapp"} {
			if v := convertToWhatsAppFormat(getStr(c, key)); v != "" {
				contactPhone = v
				break
			}
		}
	}
	if contactPhone == "" {
		contactPhone = getStr(c, "home_phone_number")
	}

	return vcard, displayName, contactPhone, nil
}

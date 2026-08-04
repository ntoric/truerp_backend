package utils

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var storePincodeRE = regexp.MustCompile(`^[1-9][0-9]{5}$`)
var storeCodeRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type StoreFormInput struct {
	Name        string
	Code        string
	Description string
	Address     string
	City        string
	State       string
	Pincode     string
	Phone       string
	Email       string
}

type NormalizedStoreForm struct {
	Name        string
	Code        string
	Description string
	Address     string
	City        string
	State       string
	Pincode     string
	Phone       string
	Email       string
}

type StoreUserFormInput struct {
	Name     string
	Email    string
	Password string
	Phone    string
	Role     string
}

type NormalizedStoreUserForm struct {
	Name     string
	Email    string
	Password string
	Phone    string
	Role     string
}

func ValidateStoreForm(input StoreFormInput, requireName bool) (NormalizedStoreForm, map[string]string) {
	fields := map[string]string{}
	out := NormalizedStoreForm{}

	name := strings.TrimSpace(input.Name)
	if requireName && name == "" {
		fields["name"] = "Store name is required"
	} else if name != "" {
		if utf8.RuneCountInString(name) < 2 {
			fields["name"] = "Store name must be at least 2 characters"
		} else if utf8.RuneCountInString(name) > 120 {
			fields["name"] = "Store name must be 120 characters or less"
		}
	}
	out.Name = name

	codeRaw := strings.TrimSpace(input.Code)
	if codeRaw != "" {
		code := NormalizeStoreCode(codeRaw, "")
		if code == "" || !storeCodeRE.MatchString(code) {
			fields["code"] = "Store code may only contain letters, numbers, and hyphens"
		} else if len(code) > 32 {
			fields["code"] = "Store code must be 32 characters or less"
		} else {
			out.Code = code
		}
	}

	desc := strings.TrimSpace(input.Description)
	if utf8.RuneCountInString(desc) > 500 {
		fields["description"] = "Description must be 500 characters or less"
	}
	out.Description = desc

	address := strings.TrimSpace(input.Address)
	if utf8.RuneCountInString(address) > 300 {
		fields["address"] = "Address must be 300 characters or less"
	}
	out.Address = address

	city := strings.TrimSpace(input.City)
	if utf8.RuneCountInString(city) > 100 {
		fields["city"] = "City must be 100 characters or less"
	}
	out.City = city

	state := strings.TrimSpace(input.State)
	if utf8.RuneCountInString(state) > 100 {
		fields["state"] = "State must be 100 characters or less"
	}
	out.State = state

	pincode := strings.TrimSpace(input.Pincode)
	if pincode != "" && !storePincodeRE.MatchString(pincode) {
		fields["pincode"] = "Enter a valid 6-digit pincode"
	}
	out.Pincode = pincode

	phone, phoneErr := ValidateAuthPhone(input.Phone)
	if phoneErr != "" {
		fields["phone"] = phoneErr
	}
	out.Phone = phone

	emailRaw := strings.TrimSpace(input.Email)
	if emailRaw != "" {
		email, emailErr := ValidateAuthEmail(emailRaw)
		if emailErr != "" {
			fields["email"] = emailErr
		} else {
			out.Email = email
		}
	}

	return out, fields
}

func ValidateStoreUserForm(input StoreUserFormInput) (NormalizedStoreUserForm, map[string]string) {
	fields := map[string]string{}
	out := NormalizedStoreUserForm{}

	name, nameErr := ValidateAuthName(input.Name)
	if nameErr != "" {
		fields["name"] = nameErr
	}
	out.Name = name

	email, emailErr := ValidateAuthEmail(input.Email)
	if emailErr != "" {
		fields["email"] = emailErr
	}
	out.Email = email

	if passwordErr := ValidateAuthPassword(input.Password); passwordErr != "" {
		fields["password"] = passwordErr
	}
	out.Password = input.Password

	phone, phoneErr := ValidateAuthPhone(input.Phone)
	if phoneErr != "" {
		fields["phone"] = phoneErr
	}
	out.Phone = phone

	role := strings.TrimSpace(strings.ToLower(input.Role))
	switch role {
	case "admin", "manager", "accountant", "staff":
		out.Role = role
	case "":
		fields["role"] = "Role is required"
	default:
		fields["role"] = "Select a valid role"
	}

	return out, fields
}

func FirstFieldMessage(fields map[string]string) string {
	for _, msg := range fields {
		return msg
	}
	return "Please fix the highlighted fields"
}

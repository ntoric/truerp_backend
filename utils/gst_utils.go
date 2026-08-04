package utils

import (
	"regexp"
	"strconv"
	"strings"
)

// Indian state codes for GST
var StateCodes = map[string]string{
	"01": "Jammu and Kashmir",
	"02": "Himachal Pradesh",
	"03": "Punjab",
	"04": "Chandigarh",
	"05": "Uttarakhand",
	"06": "Haryana",
	"07": "Delhi",
	"08": "Rajasthan",
	"09": "Uttar Pradesh",
	"10": "Bihar",
	"11": "Sikkim",
	"12": "Arunachal Pradesh",
	"13": "Nagaland",
	"14": "Manipur",
	"15": "Mizoram",
	"16": "Tripura",
	"17": "Meghalaya",
	"18": "Assam",
	"19": "West Bengal",
	"20": "Jharkhand",
	"21": "Odisha",
	"22": "Chhattisgarh",
	"23": "Madhya Pradesh",
	"24": "Gujarat",
	"25": "Daman and Diu",
	"26": "Dadra and Nagar Haveli",
	"27": "Maharashtra",
	"28": "Andhra Pradesh",
	"29": "Karnataka",
	"30": "Goa",
	"31": "Lakshadweep",
	"32": "Kerala",
	"33": "Tamil Nadu",
	"34": "Puducherry",
	"35": "Andaman and Nicobar Islands",
	"36": "Telangana",
	"37": "Andhra Pradesh (New)",
	"38": "Ladakh",
}

// ValidateGSTIN validates a GSTIN (Goods and Services Tax Identification Number)
// Format: 2 character state code + 10 digit PAN + 1 character entity code + 1 alphanumeric character + 1 check digit
// Example: 27ABCDE1234F1Z5
func ValidateGSTIN(gstin string) bool {
	if gstin == "" {
		return false
	}

	// Remove any spaces and convert to uppercase
	gstin = strings.ToUpper(strings.TrimSpace(gstin))

	// Check length (should be 15 characters)
	if len(gstin) != 15 {
		return false
	}

	// Check if all characters are alphanumeric
	matched, _ := regexp.MatchString(`^[0-9A-Z]{15}$`, gstin)
	if !matched {
		return false
	}

	// Validate state code (first 2 characters)
	stateCode := gstin[0:2]
	if _, valid := StateCodes[stateCode]; !valid {
		return false
	}

	// Validate PAN (characters 3-12) - should be 10 characters following PAN format
	pan := gstin[2:12]
	if !validatePAN(pan) {
		return false
	}

	// Validate entity code (13th character)
	entityCode := gstin[12:13]
	if !validateEntityCode(entityCode) {
		return false
	}

	// Validate check digit using mod 97 algorithm
	checkDigit := calculateCheckDigit(gstin[0:14])
	if checkDigit != gstin[14:15] {
		return false
	}

	return true
}

// validatePAN validates the PAN portion of GSTIN
func validatePAN(pan string) bool {
	// PAN format: 5 letters + 4 digits + 1 letter
	matched, _ := regexp.MatchString(`^[A-Z]{5}[0-9]{4}[A-Z]$`, pan)
	return matched
}

// validateEntityCode validates the entity code (13th character of GSTIN)
func validateEntityCode(code string) bool {
	// Valid entity codes: P (Proprietorship), C (Company), F (Firm), H (HUF), etc.
	validCodes := map[string]bool{
		"P": true, // Proprietorship
		"C": true, // Company
		"F": true, // Firm
		"H": true, // HUF
		"A": true, // AOP
		"T": true, // Trust
		"B": true, // Body of Individuals
		"L": true, // Local Authority
		"J": true, // Artificial Juridical Person
		"G": true, // Government
		"Z": true, // Others
	}
	return validCodes[code]
}

// calculateCheckDigit calculates the check digit for GSTIN using mod 97 algorithm
func calculateCheckDigit(gstinWithoutCheckDigit string) string {
	// Convert characters to their ASCII values
	var sum int
	for i, char := range gstinWithoutCheckDigit {
		var value int
		if char >= '0' && char <= '9' {
			value = int(char - '0')
		} else if char >= 'A' && char <= 'Z' {
			value = int(char - 'A' + 10)
		}
		
		// Apply weight based on position
		if i%2 == 0 {
			sum += value
		} else {
			sum += value * 2
		}
	}
	
	// Calculate check digit
	remainder := sum % 36
	checkDigit := strconv.Itoa(remainder)
	if remainder >= 10 {
		checkDigit = string('A' + remainder - 10)
	}
	
	return checkDigit
}

// GetStateCodeFromGSTIN extracts the state code from GSTIN
func GetStateCodeFromGSTIN(gstin string) string {
	if len(gstin) >= 2 {
		return gstin[0:2]
	}
	return ""
}

// GetStateNameFromCode returns the state name for a given state code
func GetStateNameFromCode(code string) string {
	if name, exists := StateCodes[code]; exists {
		return name
	}
	return ""
}

// DeterminePlaceOfSupply determines the place of supply based on buyer and seller state codes
// Returns the state code of the place of supply
func DeterminePlaceOfSupply(buyerStateCode, sellerStateCode string) string {
	// If buyer state is valid, place of supply is buyer's state
	if _, valid := StateCodes[buyerStateCode]; valid {
		return buyerStateCode
	}
	
	// Fallback to seller's state
	if _, valid := StateCodes[sellerStateCode]; valid {
		return sellerStateCode
	}
	
	// Default to seller's state code
	return sellerStateCode
}

// IsInterStateTransaction determines if a transaction is inter-state
func IsInterStateTransaction(buyerStateCode, sellerStateCode string) bool {
	return buyerStateCode != sellerStateCode
}

// GetTaxType returns the tax type based on whether transaction is inter-state or intra-state
// Returns "IGST" for inter-state, "CGST_SGST" for intra-state
func GetTaxType(buyerStateCode, sellerStateCode string) string {
	if IsInterStateTransaction(buyerStateCode, sellerStateCode) {
		return "IGST"
	}
	return "CGST_SGST"
}

// CalculateGST calculates GST based on taxable value, tax rate, and tax type
// Returns (CGST, SGST, IGST, TotalTax)
func CalculateGST(taxableValue, taxRate float64, taxType string) (float64, float64, float64, float64) {
	totalTax := taxableValue * (taxRate / 100)
	
	if taxType == "IGST" {
		return 0, 0, totalTax, totalTax
	}
	
	// Split equally between CGST and SGST for intra-state
	cgst := totalTax / 2
	sgst := totalTax / 2
	
	return cgst, sgst, 0, totalTax
}

// FormatGSTIN formats GSTIN in standard format (e.g., 27ABCDE1234F1Z5)
func FormatGSTIN(gstin string) string {
	gstin = strings.ToUpper(strings.TrimSpace(gstin))
	
	// Add spaces for readability (optional)
	if len(gstin) == 15 {
		return gstin[0:2] + gstin[2:12] + gstin[12:13] + gstin[13:14] + gstin[14:15]
	}
	
	return gstin
}

// MaskGSTIN masks GSTIN for display purposes (shows first 2 and last 4 characters)
// Example: 27******1Z5
func MaskGSTIN(gstin string) string {
	if len(gstin) != 15 {
		return gstin
	}
	return gstin[0:2] + "********" + gstin[11:15]
}

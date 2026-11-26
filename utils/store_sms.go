package utils

import (
	"fmt"
	"strings"
	"truerp/models"
)

// SendStoreSMS sends a plain-text SMS through the store's own SMS provider
// configured in Developer Settings → Integrations (DeveloperSettings). It
// reuses the platform notification senders, mapping the store's decrypted
// credentials onto their config/secret maps. Supported: twilio, msg91,
// textlocal. AWS SNS and SendGrid are reported as unsupported rather than
// silently "succeeding".
func SendStoreSMS(dev models.DeveloperSettings, to, message string) error {
	decrypt := func(enc, name string) (string, error) {
		if enc == "" {
			return "", nil
		}
		v, err := Decrypt(enc)
		if err != nil {
			return "", fmt.Errorf("failed to decrypt %s; re-save it in Developer Settings", name)
		}
		return v, nil
	}

	provider := strings.ToLower(strings.TrimSpace(dev.SMSProvider))
	if provider == "" {
		provider = "twilio"
	}
	conf := map[string]string{"provider": provider}
	secrets := map[string]string{}

	switch provider {
	case "twilio":
		token, err := decrypt(dev.EncryptedTwilioSMSAuthToken, "Twilio auth token")
		if err != nil {
			return err
		}
		conf["twilio_account_sid"] = strings.TrimSpace(dev.TwilioSMSAccountSID)
		conf["twilio_from"] = strings.TrimSpace(dev.TwilioSMSPhoneNumber)
		secrets["twilio_auth_token"] = token
		if !strings.HasPrefix(to, "+") {
			to = "+" + to
		}
	case "msg91":
		key, err := decrypt(dev.EncryptedMsg91AuthKey, "MSG91 auth key")
		if err != nil {
			return err
		}
		conf["sender_id"] = strings.TrimSpace(dev.Msg91SenderID)
		secrets["auth_key"] = key
	case "textlocal":
		key, err := decrypt(dev.EncryptedTextLocalAPIKey, "TextLocal API key")
		if err != nil {
			return err
		}
		conf["sender"] = strings.TrimSpace(dev.TextLocalSenderID)
		secrets["api_key"] = key
	default:
		return fmt.Errorf("SMS provider %q is not supported for campaigns yet (use Twilio, MSG91 or TextLocal)", provider)
	}
	return sendSMS(conf, secrets, to, message)
}

package notify

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"os"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	shoutrrrsmtp "github.com/nicholas-fedor/shoutrrr/pkg/services/email/smtp"
	mail "github.com/wneessen/go-mail"
)

func sendEmail(rawURL string, notification Notification, serviceRouter *router.ServiceRouter) error {
	service, err := serviceRouter.Locate(rawURL)
	if err != nil {
		return err
	}
	smtpService, ok := service.(*shoutrrrsmtp.Service)
	if !ok {
		return fmt.Errorf("notification target is not SMTP")
	}
	config := smtpService.Config.Clone()
	config.FixEmailTags()
	message, err := buildEmailMessage(notification, &config)
	if err != nil {
		return err
	}
	client, err := mail.NewClient(config.Host, smtpClientOptions(&config)...)
	if err != nil {
		return err
	}
	return client.DialAndSend(message)
}

func buildEmailMessage(notification Notification, config *shoutrrrsmtp.Config) (*mail.Msg, error) {
	rendered, err := renderEmailForDelivery(notification)
	if err != nil {
		return nil, err
	}
	message := mail.NewMsg()
	if config.FromName == "" {
		err = message.From(config.FromAddress)
	} else {
		err = message.FromFormat(config.FromName, config.FromAddress)
	}
	if err != nil {
		return nil, err
	}
	if err := message.To(config.ToAddresses...); err != nil {
		return nil, err
	}
	message.Subject(config.Subject)
	message.SetBodyString(mail.TypeTextPlain, plainMessage(notification))
	message.AddAlternativeString(mail.TypeTextHTML, rendered.html)
	for _, embed := range rendered.embeds {
		if err := message.EmbedReader(embed.name, bytes.NewReader(embed.data),
			mail.WithFileContentType(mail.ContentType(embed.mime))); err != nil {
			return nil, err
		}
	}
	return message, nil
}

func smtpClientOptions(config *shoutrrrsmtp.Config) []mail.Option {
	clientHost := config.ClientHost
	if clientHost == "auto" {
		clientHost, _ = os.Hostname()
	}
	if clientHost == "" {
		clientHost = "localhost"
	}
	options := []mail.Option{
		mail.WithPort(int(config.Port)),
		mail.WithTimeout(config.Timeout),
		mail.WithHELO(clientHost),
	}
	if config.Username != "" {
		options = append(options, mail.WithUsername(config.Username), mail.WithPassword(config.Password))
	}
	switch config.Auth {
	case shoutrrrsmtp.AuthTypes.Plain:
		options = append(options, mail.WithSMTPAuth(mail.SMTPAuthPlain))
	case shoutrrrsmtp.AuthTypes.CRAMMD5:
		options = append(options, mail.WithSMTPAuth(mail.SMTPAuthCramMD5))
	case shoutrrrsmtp.AuthTypes.OAuth2:
		options = append(options, mail.WithSMTPAuth(mail.SMTPAuthXOAUTH2))
	default:
		options = append(options, mail.WithSMTPAuth(mail.SMTPAuthNoAuth))
	}
	implicitTLS := config.Encryption == shoutrrrsmtp.EncMethods.ImplicitTLS ||
		(config.Encryption == shoutrrrsmtp.EncMethods.Auto && config.Port == shoutrrrsmtp.ImplicitTLSPort)
	if implicitTLS {
		options = append(options, mail.WithSSL())
	} else if config.UseStartTLS {
		policy := mail.TLSOpportunistic
		if config.RequireStartTLS {
			policy = mail.TLSMandatory
		}
		options = append(options, mail.WithTLSPolicy(policy))
	} else {
		options = append(options, mail.WithTLSPolicy(mail.NoTLS))
	}
	if config.SkipTLSVerify {
		// #nosec G402 The administrator explicitly enabled certificate verification bypass.
		options = append(options, mail.WithTLSConfig(&tls.Config{
			ServerName: config.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
		}))
	}
	return options
}

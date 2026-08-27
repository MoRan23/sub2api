package service

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

type smtpMessage struct {
	envelopeFrom string
	envelopeTo   string
	messageID    string
	data         []byte
}

func buildSMTPMessage(config *SMTPConfig, to, subject, body string) (smtpMessage, error) {
	return buildSMTPMessageWithOptions(config, to, subject, body, EmailSendOptions{})
}

type EmailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type EmailSendOptions struct {
	MessageID  string
	ReplyTo    string
	Attachment *EmailAttachment
}

func buildSMTPMessageWithOptions(config *SMTPConfig, to, subject, body string, options EmailSendOptions) (smtpMessage, error) {
	if config == nil {
		return smtpMessage{}, errors.New("missing SMTP configuration")
	}

	fromAddress, err := parseSMTPAddress(config.From, "from")
	if err != nil {
		return smtpMessage{}, err
	}
	recipientAddress, err := parseSMTPAddress(to, "recipient")
	if err != nil {
		return smtpMessage{}, err
	}
	messageID := strings.TrimSpace(options.MessageID)
	if messageID == "" {
		messageID, err = generateEmailMessageID(fromAddress.Address, config.Host)
		if err != nil {
			return smtpMessage{}, fmt.Errorf("generate message ID: %w", err)
		}
	}
	if strings.ContainsAny(messageID, "\r\n") || !strings.HasPrefix(messageID, "<") || !strings.HasSuffix(messageID, ">") {
		return smtpMessage{}, errors.New("invalid email message ID")
	}

	fromName := sanitizeEmailHeader(config.FromName)
	if strings.TrimSpace(fromName) == "" {
		fromName = fromAddress.Name
	}
	fromHeader := (&mail.Address{
		Name:    fromName,
		Address: fromAddress.Address,
	}).String()
	toHeader := (&mail.Address{
		Name:    recipientAddress.Name,
		Address: recipientAddress.Address,
	}).String()
	subjectHeader := mime.QEncoding.Encode("UTF-8", sanitizeEmailHeader(subject))

	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&message, "To: %s\r\n", toHeader)
	fmt.Fprintf(&message, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&message, "Message-ID: %s\r\n", messageID)
	if strings.TrimSpace(options.ReplyTo) != "" {
		replyTo, err := parseSMTPAddress(options.ReplyTo, "reply-to")
		if err != nil {
			return smtpMessage{}, err
		}
		fmt.Fprintf(&message, "Reply-To: %s\r\n", replyTo.String())
	}
	fmt.Fprintf(&message, "Subject: %s\r\n", subjectHeader)
	fmt.Fprint(&message, "MIME-Version: 1.0\r\n")
	if options.Attachment == nil {
		fmt.Fprint(&message, "Content-Type: text/html; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		bodyWriter := quotedprintable.NewWriter(&message)
		if _, err := bodyWriter.Write([]byte(body)); err != nil {
			return smtpMessage{}, fmt.Errorf("encode email body: %w", err)
		}
		if err := bodyWriter.Close(); err != nil {
			return smtpMessage{}, fmt.Errorf("close email body encoder: %w", err)
		}
	} else {
		attachment := options.Attachment
		if len(attachment.Data) == 0 || strings.ContainsAny(attachment.Filename, "\r\n") {
			return smtpMessage{}, errors.New("invalid email attachment")
		}
		contentType := strings.TrimSpace(attachment.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		writer := multipart.NewWriter(&message)
		fmt.Fprintf(&message, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", writer.Boundary())
		bodyHeader := make(textproto.MIMEHeader)
		bodyHeader.Set("Content-Type", "text/html; charset=UTF-8")
		bodyHeader.Set("Content-Transfer-Encoding", "quoted-printable")
		bodyPart, err := writer.CreatePart(bodyHeader)
		if err != nil {
			return smtpMessage{}, fmt.Errorf("create email body part: %w", err)
		}
		bodyWriter := quotedprintable.NewWriter(bodyPart)
		if _, err := bodyWriter.Write([]byte(body)); err != nil {
			return smtpMessage{}, fmt.Errorf("encode email body: %w", err)
		}
		if err := bodyWriter.Close(); err != nil {
			return smtpMessage{}, fmt.Errorf("close email body encoder: %w", err)
		}
		filename := mime.QEncoding.Encode("UTF-8", attachment.Filename)
		attachmentHeader := make(textproto.MIMEHeader)
		attachmentHeader.Set("Content-Type", fmt.Sprintf("%s; name=%q", contentType, filename))
		attachmentHeader.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		attachmentHeader.Set("Content-Transfer-Encoding", "base64")
		attachmentPart, err := writer.CreatePart(attachmentHeader)
		if err != nil {
			return smtpMessage{}, fmt.Errorf("create email attachment part: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(attachment.Data)
		for len(encoded) > 76 {
			if _, err := fmt.Fprintf(attachmentPart, "%s\r\n", encoded[:76]); err != nil {
				return smtpMessage{}, fmt.Errorf("write email attachment: %w", err)
			}
			encoded = encoded[76:]
		}
		if _, err := fmt.Fprintf(attachmentPart, "%s\r\n", encoded); err != nil {
			return smtpMessage{}, fmt.Errorf("write email attachment: %w", err)
		}
		if err := writer.Close(); err != nil {
			return smtpMessage{}, fmt.Errorf("close multipart email: %w", err)
		}
	}

	return smtpMessage{
		envelopeFrom: fromAddress.Address,
		envelopeTo:   recipientAddress.Address,
		messageID:    messageID,
		data:         message.Bytes(),
	}, nil
}

func parseSMTPAddress(value, field string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("invalid SMTP %s address: contains a line break", field)
	}

	cleaned := strings.TrimSpace(value)
	address, err := mail.ParseAddress(cleaned)
	if err != nil || strings.TrimSpace(address.Address) == "" {
		if err == nil {
			err = fmt.Errorf("address is empty")
		}
		return nil, fmt.Errorf("invalid SMTP %s address: %w", field, err)
	}
	return address, nil
}

func generateEmailMessageID(fromAddress, smtpHost string) (string, error) {
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", err
	}

	domain := strings.TrimSpace(sanitizeEmailHeader(smtpHost))
	if at := strings.LastIndexByte(fromAddress, '@'); at >= 0 && at < len(fromAddress)-1 {
		domain = fromAddress[at+1:]
	}
	domain = strings.Trim(domain, "[]<>")
	if domain == "" {
		domain = "localhost"
	}

	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(randomID), domain), nil
}

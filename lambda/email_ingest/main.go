package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jhillyerd/enmime"
)

// s3API captures the subset of the S3 client API we use. This enables unit testing with a mock.
type s3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

var newS3Client = func(ctx context.Context) (s3API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, evt events.S3Event) error {
	bucketName := os.Getenv("EMAIL_BUCKET")
	if bucketName == "" {
		return fmt.Errorf("EMAIL_BUCKET env var is required")
	}
	incomingPrefix := envOr("INCOMING_PREFIX", "raw/email/incoming/")
	rawEmailBase := envOr("RAW_EMAIL_BASE", "raw/email/")
	rawCsvBase := envOr("RAW_CSV_BASE", "raw/loseit_csv/")

	s3c, err := newS3Client(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	for _, rec := range evt.Records {
		b := rec.S3.Bucket.Name
		k, err := urlDecode(rec.S3.Object.Key)
		if err != nil {
			return err
		}
		// Only process our incoming prefix
		if !strings.HasPrefix(k, incomingPrefix) {
			log.Printf("skip key without incoming prefix: %s", k)
			continue
		}

		// Fetch the raw EML
		obj, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: &b, Key: &k})
		if err != nil {
			return fmt.Errorf("s3 get %s/%s: %w", b, k, err)
		}
		rawBytes, err := io.ReadAll(obj.Body)
		if err != nil {
			return fmt.Errorf("read s3 object: %w", err)
		}
		_ = obj.Body.Close()

		// Parse headers for Message-ID and Date
		msg, _ := mail.ReadMessage(bytes.NewReader(rawBytes))

		// Check if email is from allowed domain (loseit.com)
		allowedDomain := envOr("ALLOWED_SENDER_DOMAIN", "loseit.com")

		if allowedDomain != "" {
			fromHeader := msg.Header.Get("From")
			if fromHeader == "" {
				log.Printf("warn: no From header found, deleting email from S3")
				if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b, Key: &k}); err != nil {
					log.Printf("error: failed to delete email %s/%s: %v", b, k, err)
				} else {
					log.Printf("info: deleted email %s/%s (no From header)", b, k)
				}
				continue
			}

			// Parse email address to extract domain
			fromAddr, err := mail.ParseAddress(fromHeader)
			if err != nil {
				log.Printf("warn: failed to parse From address '%s': %v, deleting email", fromHeader, err)
				if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b, Key: &k}); err != nil {
					log.Printf("error: failed to delete email %s/%s: %v", b, k, err)
				} else {
					log.Printf("info: deleted email %s/%s (invalid From header)", b, k)
				}
				continue
			}

			// Extract domain from email address
			parts := strings.Split(fromAddr.Address, "@")
			if len(parts) != 2 {
				log.Printf("warn: invalid email format '%s', deleting email", fromAddr.Address)
				if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b, Key: &k}); err != nil {
					log.Printf("error: failed to delete email %s/%s: %v", b, k, err)
				} else {
					log.Printf("info: deleted email %s/%s (invalid email format)", b, k)
				}
				continue
			}
			senderDomain := strings.ToLower(parts[1])

			if senderDomain != strings.ToLower(allowedDomain) {
				log.Printf("info: email from domain '%s' not allowed (expected '%s'), deleting email", senderDomain, allowedDomain)
				if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b, Key: &k}); err != nil {
					log.Printf("error: failed to delete email %s/%s: %v", b, k, err)
				} else {
					log.Printf("info: deleted email %s/%s from unauthorized domain '%s'", b, k, senderDomain)
				}
				continue
			}
			log.Printf("info: email from allowed domain '%s', processing", senderDomain)
		}

		messageID := sanitizeMessageID(msg)
		if messageID == "" {
			messageID = uuid.New().String()
		}
		dt := dateFromMessage(msg)

		// Always write raw EML to partitioned path raw/email/year=YYYY/month=MM/day=DD/<messageID>.eml
		year, month, day := dateParts(dt)
		rawKey := fmt.Sprintf("%syear=%s/month=%s/day=%s/%s.eml", rawEmailBase, year, month, day, messageID)
		if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &bucketName,
			Key:         &rawKey,
			Body:        bytes.NewReader(rawBytes),
			ContentType: aws.String("message/rfc822"),
			ACL:         s3types.ObjectCannedACLPrivate,
		}); err != nil {
			return fmt.Errorf("put raw eml: %w", err)
		}

		// Extract CSV attachments using enmime
		env, err := enmime.ReadEnvelope(bytes.NewReader(rawBytes))
		if err != nil {
			log.Printf("warn: enmime parse failed (%v); continuing with raw only", err)
		} else {
			for _, a := range env.Attachments {
				ctype, _, _ := mime.ParseMediaType(a.ContentType)
				name := a.FileName
				if strings.EqualFold(filepath.Ext(name), ".csv") || strings.EqualFold(ctype, "text/csv") {
					data := a.Content
					if data == nil {
						log.Printf("warn: attachment %s has no content", name)
						continue
					}
					// Desired path: raw/loseit_csv/year=YYYY/month=MM/day=DD/loseit-daily.csv (immutable)
					// To avoid collisions if multiple emails per day, append index if key exists.
					baseName := "loseit-daily.csv"
					if sn := strings.TrimSpace(name); sn != "" {
						baseName = sanitizeFilename(sn)
					}
					csvKey := fmt.Sprintf("%syear=%s/month=%s/day=%s/%s", rawCsvBase, year, month, day, baseName)
					// If object exists, append suffix -2, -3, ...
					csvKey = ensureUniqueKey(ctx, s3c, bucketName, csvKey)
					if _, perr := s3c.PutObject(ctx, &s3.PutObjectInput{
						Bucket:      &bucketName,
						Key:         &csvKey,
						Body:        bytes.NewReader(data),
						ContentType: aws.String("text/csv"),
						ACL:         s3types.ObjectCannedACLPrivate,
					}); perr != nil {
						log.Printf("warn: put csv %s: %v", csvKey, perr)
					}
				}
			}
		}

		// Do NOT delete original: raw email is immutable audit trail.
	}
	return nil
}

func urlDecode(s string) (string, error) {
	// S3 event keys can be URL-encoded; handle spaces and special chars
	r := strings.ReplaceAll(s, "+", "%20")
	u, err := urlUnescape(r)
	if err != nil {
		return s, nil // best-effort
	}
	return u, nil
}

// urlUnescape is isolated to avoid pulling net/url just for unescape
func urlUnescape(s string) (string, error) {
	// simplified unescape for %xx sequences
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var hv byte
			for j := 1; j <= 2; j++ {
				hv <<= 4
				c := s[i+j]
				switch {
				case '0' <= c && c <= '9':
					hv |= c - '0'
				case 'a' <= c && c <= 'f':
					hv |= c - 'a' + 10
				case 'A' <= c && c <= 'F':
					hv |= c - 'A' + 10
				default:
					return "", fmt.Errorf("invalid escape")
				}
			}
			out = append(out, hv)
			i += 2
		} else {
			out = append(out, s[i])
		}
	}
	return string(out), nil
}

func sanitizeMessageID(msg *mail.Message) string {
	if msg == nil {
		return ""
	}
	mid := msg.Header.Get("Message-ID")
	mid = strings.TrimSpace(strings.Trim(mid, "<>"))
	if mid == "" {
		return ""
	}
	// Replace non word chars with dash
	re := regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	return re.ReplaceAllString(mid, "-")
}

func sanitizeFilename(name string) string {
	if name == "" {
		return "attachment.csv"
	}
	name = filepath.Base(name)
	re := regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	return re.ReplaceAllString(name, "_")
}

func dateFromMessage(msg *mail.Message) string {
	// Prefer Date header; fallback to now UTC
	t := time.Now().UTC()
	if msg != nil {
		if dh := msg.Header.Get("Date"); dh != "" {
			if dt, err := mail.ParseDate(dh); err == nil {
				t = dt.UTC()
			}
		}
	}
	return t.Format("2006-01-02")
}

func dateParts(dt string) (string, string, string) {
	// dt format: YYYY-MM-DD
	parts := strings.Split(dt, "-")
	if len(parts) != 3 {
		now := time.Now().UTC()
		return now.Format("2006"), now.Format("01"), now.Format("02")
	}
	return parts[0], parts[1], parts[2]
}

func ensureUniqueKey(ctx context.Context, s3c s3API, bucket, key string) string {
	// If key exists, append -2, -3, ... before extension
	base := key
	ext := ""
	if i := strings.LastIndex(key, "."); i > -1 {
		base = key[:i]
		ext = key[i:]
	}
	try := 1
	k := key
	for {
		_, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &k})
		if err != nil {
			// assume not found
			return k
		}
		try++
		k = fmt.Sprintf("%s-%d%s", base, try, ext)
		if try > 50 {
			return fmt.Sprintf("%s-%s%s", base, uuid.New().String(), ext)
		}
	}
}

package main

import (
    "bytes"
    "context"
    "fmt"
    "io"
    "net/mail"
    "os"
    "path/filepath"
    "strings"
    "testing"
    
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    "github.com/aws/aws-lambda-go/events"
)

func TestUrlUnescape(t *testing.T) {
    got, err := urlUnescape("a%2Bb%20test%2Ffile")
    if err != nil { t.Fatalf("unexpected err: %v", err) }
    want := "a+b test/file"
    if got != want { t.Fatalf("got %q want %q", got, want) }
}

func TestUrlDecode_PlusToSpace(t *testing.T) {
    got, _ := urlDecode("foo+bar%2Fbaz")
    if got != "foo bar/baz" {
        t.Fatalf("got %q want %q", got, "foo bar/baz")
    }
}

func TestSanitizeMessageID(t *testing.T) {
    raw := "Message-ID: <abc.def@domain.com>\r\n\r\nBody"
    msg, err := mail.ReadMessage(strings.NewReader(raw))
    if err != nil { t.Fatalf("read msg: %v", err) }
    got := sanitizeMessageID(msg)
    want := "abc.def-domain.com"
    if got != want { t.Fatalf("got %q want %q", got, want) }
}

func TestSanitizeFilename(t *testing.T) {
    cases := map[string]string{
        "../../etc/passwd": "passwd",
        "my file.csv":      "my_file.csv",
        "We!rd@Name#.csv":  "We_rd_Name_.csv",
    }
    for in, want := range cases {
        if got := sanitizeFilename(in); got != want {
            t.Fatalf("in %q got %q want %q", in, got, want)
        }
    }
}

func TestDateFromMessage(t *testing.T) {
    raw := "Date: Wed, 27 Aug 2025 12:34:56 -0700\r\n\r\nBody"
    msg, err := mail.ReadMessage(bytes.NewReader([]byte(raw)))
    if err != nil { t.Fatalf("read msg: %v", err) }
    got := dateFromMessage(msg)
    if got != "2025-08-27" {
        t.Fatalf("got %q want %q", got, "2025-08-27")
    }
}

func TestDateParts(t *testing.T) {
    y, m, d := dateParts("2025-08-27")
    if y != "2025" || m != "08" || d != "27" {
        t.Fatalf("got %s-%s-%s", y, m, d)
    }
}

// --- Integration-ish unit test with mocked S3 ---

type putCall struct{ Key string; Body []byte; ContentType string }
type mockS3 struct {
    // get returns this body for any GetObject
    getBody []byte
    puts    []putCall
}

func (m *mockS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
    rc := io.NopCloser(bytes.NewReader(m.getBody))
    return &s3.GetObjectOutput{Body: rc}, nil
}
func (m *mockS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
    b, _ := io.ReadAll(in.Body)
    ct := ""
    if in.ContentType != nil { ct = *in.ContentType }
    m.puts = append(m.puts, putCall{Key: aws.ToString(in.Key), Body: b, ContentType: ct})
    return &s3.PutObjectOutput{}, nil
}
func (m *mockS3) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
    // Simulate not found so ensureUniqueKey uses the initial name
    return nil, fmt.Errorf("not found")
}

func TestHandler_ExtractsCSVFromEML(t *testing.T) {
    // Load example EML
    emlPath := filepath.Join(".", "loseit_example.eml")
    eml, err := os.ReadFile(emlPath)
    if err != nil { t.Fatalf("read eml: %v", err) }

    // Prep mock and inject it
    mock := &mockS3{getBody: eml}
    old := newS3Client
    newS3Client = func(ctx context.Context) (s3API, error) { return mock, nil }
    defer func(){ newS3Client = old }()

    // Set envs
    t.Setenv("EMAIL_BUCKET", "test-bucket")
    t.Setenv("INCOMING_PREFIX", "raw/email/incoming/")
    t.Setenv("RAW_EMAIL_BASE", "raw/email/")
    t.Setenv("RAW_CSV_BASE", "raw/loseit_csv/")

    // Build S3 event
    evt := events.S3Event{Records: []events.S3EventRecord{{
        S3: events.S3Entity{
            Bucket: events.S3Bucket{Name: "test-bucket"},
            Object: events.S3Object{Key: "raw/email/incoming/loseit.eml"},
        },
    }}}

    // Run handler
    if err := handler(context.Background(), evt); err != nil {
        t.Fatalf("handler error: %v", err)
    }

    // Validate we wrote raw EML and CSV
    var gotRaw, gotCSV *putCall
    for i := range mock.puts {
        pc := &mock.puts[i]
        if strings.HasPrefix(pc.Key, "raw/email/year=") && strings.HasSuffix(pc.Key, ".eml") {
            gotRaw = pc
        }
        if strings.HasPrefix(pc.Key, "raw/loseit_csv/year=") && strings.HasSuffix(pc.Key, ".csv") {
            gotCSV = pc
        }
    }
    if gotRaw == nil { t.Fatalf("expected raw EML put, none found: %#v", mock.puts) }
    if gotCSV == nil { t.Fatalf("expected CSV put, none found: %#v", mock.puts) }
    if len(gotCSV.Body) == 0 { t.Fatalf("csv body is empty") }
}

// no-op: removed the local event types in favor of aws-lambda-go/events

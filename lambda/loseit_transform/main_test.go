package main

import (
    "bytes"
    "context"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/aws/aws-lambda-go/events"
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/service/s3"
)

type mockS3 struct {
    getBody []byte
    puts    []struct{ Key string; Body []byte; ContentType string }
}

func (m *mockS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
    return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(m.getBody))}, nil
}
func (m *mockS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
    b, _ := io.ReadAll(in.Body)
    ct := ""
    if in.ContentType != nil { ct = *in.ContentType }
    m.puts = append(m.puts, struct{ Key string; Body []byte; ContentType string }{Key: aws.ToString(in.Key), Body: b, ContentType: ct})
    return &s3.PutObjectOutput{}, nil
}

func TestHandler_TransformsCSVToParquet(t *testing.T) {
    // Load example CSV from repo
    csvPath := filepath.Join(".", "example_report.csv")
    data, err := os.ReadFile(csvPath)
    if err != nil { t.Fatalf("read csv: %v", err) }

    // Prepare mock S3 and inject
    mock := &mockS3{getBody: data}
    oldFactory := newS3Client
    newS3Client = func(ctx context.Context) (s3API, error) { return mock, nil }
    defer func(){ newS3Client = oldFactory }()

    // Environment
    t.Setenv("DATA_BUCKET", "test-bucket")
    t.Setenv("RAW_CSV_BASE", "raw/loseit_csv/")
    t.Setenv("CURATED_BASE", "curated/loseit_parquet/")

    // Invoke handler with an S3 event pointing at a date-partitioned CSV path
    key := "raw/loseit_csv/year=2025/month=08/day=27/example_report.csv"
    evt := events.S3Event{Records: []events.S3EventRecord{{
        S3: events.S3Entity{Bucket: events.S3Bucket{Name: "test-bucket"}, Object: events.S3Object{Key: key}},
    }}}

    if err := handler(context.Background(), evt); err != nil {
        t.Fatalf("handler error: %v", err)
    }

    // Expect a Parquet file at curated/loseit_parquet/year=.../part-0000.snappy.parquet with magic header
    var outKey string
    var outBody []byte
    for _, p := range mock.puts {
        if strings.HasPrefix(p.Key, "curated/loseit_parquet/year=2025/month=08/day=27/") && strings.HasSuffix(p.Key, ".parquet") {
            outKey = p.Key
            outBody = p.Body
            break
        }
    }
    if outKey == "" { t.Fatalf("did not find curated Parquet put: %#v", mock.puts) }
    if len(outBody) < 4 || string(outBody[:4]) != "PAR1" { t.Fatalf("missing Parquet magic header") }
}

func TestHandler_HandlesURLEncodedKeys(t *testing.T) {
    // Load example CSV from repo
    csvPath := filepath.Join(".", "example_report.csv")
    data, err := os.ReadFile(csvPath)
    if err != nil { t.Fatalf("read csv: %v", err) }

    // Prepare mock S3 and inject
    mock := &mockS3{getBody: data}
    oldFactory := newS3Client
    newS3Client = func(ctx context.Context) (s3API, error) { return mock, nil }
    defer func(){ newS3Client = oldFactory }()

    // Environment
    t.Setenv("DATA_BUCKET", "test-bucket")
    t.Setenv("RAW_CSV_BASE", "raw/loseit_csv/")
    t.Setenv("CURATED_BASE", "curated/loseit_parquet/")

    // Invoke handler with a URL-encoded S3 event key (like what we see in production)
    key := "raw/loseit_csv/year%3D2025/month%3D09/day%3D21/Daily_Report_39644994_20250920.csv"
    evt := events.S3Event{Records: []events.S3EventRecord{{
        S3: events.S3Entity{Bucket: events.S3Bucket{Name: "test-bucket"}, Object: events.S3Object{Key: key}},
    }}}

    if err := handler(context.Background(), evt); err != nil {
        t.Fatalf("handler error: %v", err)
    }

    // Expect a Parquet file at curated/loseit_parquet/year=.../part-0000.snappy.parquet with magic header
    var outKey string
    var outBody []byte
    for _, p := range mock.puts {
        if strings.HasPrefix(p.Key, "curated/loseit_parquet/year=2025/month=09/day=21/") && strings.HasSuffix(p.Key, ".parquet") {
            outKey = p.Key
            outBody = p.Body
            break
        }
    }
    if outKey == "" { t.Fatalf("did not find curated Parquet put: %#v", mock.puts) }
    if len(outBody) < 4 || string(outBody[:4]) != "PAR1" { t.Fatalf("missing Parquet magic header") }
}


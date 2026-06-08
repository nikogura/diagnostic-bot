# ECR Vulnerability Scanning Integration

## Overview

This document describes the ECR vulnerability scanning capability added to the diagnostic bot. The feature allows generating comprehensive vulnerability reports for container images across multiple AWS accounts.

## Investigation Template

**File:** `investigations/ecr-vulnerability-scan.yaml`

**Trigger Patterns:**
- "ecr vuln"
- "ecr vulnerability"
- "container vuln"
- "image vuln"
- "ecr report"
- "vulnerability report"

**Default Behavior:**
- Scans all configured AWS accounts
- Filters images pushed in the last 30 days
- Reports all severity levels
- Generates PDF report with remediation recommendations

## MCP Tool

**Tool Name:** `ecr_scan_results`

**Parameters:**
- `accounts` (required): Array of AWS account IDs
- `regions` (optional): Array of AWS regions (default: `["us-east-1"]`)
- `max_age_days` (optional): Filter images by age (default: 30)
- `min_severity` (optional): Minimum severity to report (CRITICAL, HIGH, MEDIUM, LOW)
- `repositories` (optional): Specific repositories to scan

**Implementation Status:** 🚧 **PARTIAL - Requires AWS SDK Integration**

The MCP tool handler is implemented with a mock data provider. To complete the integration:

## Required Dependencies

Add to `go.mod`:

```go
require (
    github.com/aws/aws-sdk-go-v2/config v1.27.0
    github.com/aws/aws-sdk-go-v2/service/ecr v1.30.0
    github.com/aws/aws-sdk-go-v2/service/sts v1.30.0
    github.com/aws/aws-sdk-go-v2/credentials v1.17.0
)
```

Install:
```bash
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/service/ecr
go get github.com/aws/aws-sdk-go-v2/service/sts
```

## Implementation Steps

### 1. Update `pkg/mcp/ecr.go` - Replace Mock Implementation

The current implementation in `queryECRInAccount()` returns mock data. Replace with:

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/ecr"
    "github.com/aws/aws-sdk-go-v2/service/ecr/types"
    "github.com/aws/aws-sdk-go-v2/service/sts"
)

func (s *Server) queryECRInAccount(
    ctx context.Context,
    accountID string,
    region string,
    maxAgeDays int,
    minSeverity string,
    repositories []string,
) (results []ECRScanResult, err error) {
    // Load AWS config
    cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
    if err != nil {
        return results, fmt.Errorf("loading AWS config: %w", err)
    }

    // Assume role if cross-account (optional)
    if accountID != "" {
        // Use STS to assume role in target account
        // cfg = assumeRoleInAccount(ctx, cfg, accountID)
    }

    // Create ECR client
    ecrClient := ecr.NewFromConfig(cfg)

    // List repositories
    repoListInput := &ecr.DescribeRepositoriesInput{}
    if len(repositories) > 0 {
        repoListInput.RepositoryNames = repositories
    }

    repoOutput, err := ecrClient.DescribeRepositories(ctx, repoListInput)
    if err != nil {
        return results, fmt.Errorf("listing ECR repositories: %w", err)
    }

    cutoffTime := time.Now().AddDate(0, 0, -maxAgeDays)

    // For each repository, get images and scan results
    for _, repo := range repoOutput.Repositories {
        repoName := *repo.RepositoryName

        // List images in repository
        imagesInput := &ecr.DescribeImagesInput{
            RepositoryName: &repoName,
            Filter: &types.DescribeImagesFilter{
                // Filter by pushed date if supported
            },
        }

        imagesOutput, err := ecrClient.DescribeImages(ctx, imagesInput)
        if err != nil {
            s.logger.WarnContext(ctx, "failed to list images in repository",
                "repository", repoName,
                "error", err.Error())
            continue
        }

        // Process each image
        for _, imageDetail := range imagesOutput.ImageDetails {
            // Skip images older than cutoff
            if imageDetail.ImagePushedAt.Before(cutoffTime) {
                continue
            }

            imageTag := "untagged"
            if len(imageDetail.ImageTags) > 0 {
                imageTag = imageDetail.ImageTags[0]
            }

            // Get scan findings
            scanInput := &ecr.DescribeImageScanFindingsInput{
                RepositoryName: &repoName,
                ImageId: &types.ImageIdentifier{
                    ImageDigest: imageDetail.ImageDigest,
                },
            }

            scanOutput, scanErr := ecrClient.DescribeImageScanFindings(ctx, scanInput)
            if scanErr != nil {
                s.logger.WarnContext(ctx, "failed to get scan findings",
                    "repository", repoName,
                    "tag", imageTag,
                    "error", scanErr.Error())
                // Record scan failure
                results = append(results, ECRScanResult{
                    AccountID:      accountID,
                    Region:         region,
                    RepositoryName: repoName,
                    ImageTag:       imageTag,
                    ImageDigest:    *imageDetail.ImageDigest,
                    PushedAt:       *imageDetail.ImagePushedAt,
                    ScanStatus:     "FAILED",
                })
                continue
            }

            // Parse scan findings
            scanResult := parseScanFindings(scanOutput, accountID, region, repoName, imageTag, imageDetail)

            // Filter by minimum severity if specified
            if shouldIncludeResult(scanResult, minSeverity) {
                results = append(results, scanResult)
            }
        }
    }

    return results, nil
}

func parseScanFindings(
    scanOutput *ecr.DescribeImageScanFindingsOutput,
    accountID string,
    region string,
    repoName string,
    imageTag string,
    imageDetail types.ImageDetail,
) ECRScanResult {
    result := ECRScanResult{
        AccountID:      accountID,
        Region:         region,
        RepositoryName: repoName,
        ImageTag:       imageTag,
        ImageDigest:    *imageDetail.ImageDigest,
        PushedAt:       *imageDetail.ImagePushedAt,
        ScanStatus:     string(scanOutput.ImageScanStatus.Status),
    }

    if scanOutput.ImageScanFindings != nil {
        // Parse severity counts
        for severity, count := range scanOutput.ImageScanFindings.FindingSeverityCounts {
            switch severity {
            case types.FindingSeverityCritical:
                result.Vulnerabilities.Critical = int(count)
            case types.FindingSeverityHigh:
                result.Vulnerabilities.High = int(count)
            case types.FindingSeverityMedium:
                result.Vulnerabilities.Medium = int(count)
            case types.FindingSeverityLow:
                result.Vulnerabilities.Low = int(count)
            case types.FindingSeverityInformational:
                result.Vulnerabilities.Informational = int(count)
            case types.FindingSeverityUndefined:
                result.Vulnerabilities.Undefined = int(count)
            }
        }

        // Parse detailed findings (only CRITICAL and HIGH for brevity)
        for _, finding := range scanOutput.ImageScanFindings.Findings {
            if finding.Severity == types.FindingSeverityCritical ||
               finding.Severity == types.FindingSeverityHigh {

                vulnFinding := VulnerabilityFinding{
                    Name:     *finding.Name,
                    Severity: string(finding.Severity),
                }

                if finding.Description != nil {
                    vulnFinding.Description = *finding.Description
                }

                if finding.Uri != nil {
                    vulnFinding.URI = *finding.Uri
                }

                // Extract CVSS score if available
                if len(finding.Attributes) > 0 {
                    for _, attr := range finding.Attributes {
                        if *attr.Key == "CVSS_SCORE" {
                            if score, err := strconv.ParseFloat(*attr.Value, 64); err == nil {
                                vulnFinding.CVSS = score
                            }
                        }
                    }
                }

                // Get affected packages
                for _, attr := range finding.Attributes {
                    if *attr.Key == "package_name" {
                        vulnFinding.Packages = append(vulnFinding.Packages, *attr.Value)
                    }
                }

                result.Findings = append(result.Findings, vulnFinding)
            }
        }
    }

    return result
}

func shouldIncludeResult(result ECRScanResult, minSeverity string) bool {
    if minSeverity == "" {
        return true
    }

    switch minSeverity {
    case "CRITICAL":
        return result.Vulnerabilities.Critical > 0
    case "HIGH":
        return result.Vulnerabilities.Critical > 0 || result.Vulnerabilities.High > 0
    case "MEDIUM":
        return result.Vulnerabilities.Critical > 0 || result.Vulnerabilities.High > 0 || result.Vulnerabilities.Medium > 0
    case "LOW":
        return result.Vulnerabilities.Critical > 0 || result.Vulnerabilities.High > 0 ||
            result.Vulnerabilities.Medium > 0 || result.Vulnerabilities.Low > 0
    default:
        return true
    }
}
```

### 2. IAM Permissions

The bot needs these IAM permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ecr:DescribeRepositories",
        "ecr:DescribeImages",
        "ecr:DescribeImageScanFindings",
        "ecr:ListImages"
      ],
      "Resource": "*"
    }
  ]
}
```

For cross-account access, set up trust relationships:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::CENTRAL_ACCOUNT:role/diagnostic-bot-role"
      },
      "Action": "sts:AssumeRole"
    }
  ]
}
```

### 3. Kubernetes Deployment Configuration

Use **IRSA (IAM Roles for Service Accounts)** for credential management:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: diagnostic-bot
  namespace: diagnostic-bot
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/diagnostic-bot-ecr-reader

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: diagnostic-bot
spec:
  template:
    spec:
      serviceAccountName: diagnostic-bot
      containers:
      - name: bot
        env:
        - name: AWS_REGION
          value: "us-east-1"
```

### 4. Update Investigation Template Variables

Before deploying, replace these variables in `investigations/ecr-vulnerability-scan.yaml`:

```yaml
# Example production values
AWS_ACCOUNTS: "123456789012,210987654321,456789012345"
DEFAULT_ACCOUNTS: "prod-account,staging-account"
ACCOUNT_MAPPING: "prod=123456789012,staging=210987654321,dev=456789012345"
ECR_REGIONS: "us-east-1,us-west-2"
EXCLUDE_REPOS: "legacy-*,archived-*,test-*"
```

Store the final template in Vault:

```bash
vault kv put infra/diagnostic-bot-inv-ecr \
  ecr-vulnerability-scan.yaml=@investigations/ecr-vulnerability-scan.yaml
```

### 5. Testing

The `ecr_scan_results` tool is exercised through the bot, not a standalone
process. With AWS credentials and `AWS_REGION` configured:

- **Via Slack** — ask the bot to run an ECR vulnerability investigation
  (e.g. "Generate an ECR vulnerability report for the last 30 days in the
  production account").
- **Via the MCP server** — start the bot with `MCP_HTTP_ENABLED=true`, point an
  MCP client (such as Claude Code) at the `/mcp` endpoint, and call
  `ecr_scan_results` with the `accounts` and `max_age_days` arguments.

## Report Output

The investigation generates:

1. **Text Summary** - Sent to Slack thread
   - Total images scanned
   - Vulnerability counts by severity
   - Top vulnerable images
   - Critical findings requiring immediate action

2. **PDF Report** - Automatically uploaded to Slack
   - Executive summary with metrics
   - Risk distribution tables
   - Top 10 most vulnerable images
   - Top 10 most common CVEs
   - Remediation roadmap with timelines
   - Detailed findings appendix

## Security Considerations

- **Read-Only Access**: Bot only queries ECR, never modifies images or triggers scans
- **Cross-Account**: Uses STS AssumeRole for multi-account access
- **Audit Trail**: All ECR API calls logged in CloudTrail
- **Data Sensitivity**: Vulnerability reports are sensitive - share via secure channels only
- **No Direct Actions**: Remediation must go through GitOps workflows

## Example Usage

In Slack:

```
@diagnostic-bot ecr vulnerability report for last 30 days
@diagnostic-bot ecr report for prod account, critical and high only
@diagnostic-bot show me vulnerable containers in staging
@diagnostic-bot ecr scan for api-gateway repository
```

## Troubleshooting

**"No images found"**:
- Check IAM permissions
- Verify account IDs are correct
- Confirm images exist within age filter
- Check AWS credentials are valid

**"Scan status: FAILED"**:
- ECR scan failed or not enabled
- Enable scan-on-push in ECR repository settings
- Manually trigger scan: `aws ecr start-image-scan`

**"GitHub token not provided"** (for fix recommendations):
- Set `GITHUB_TOKEN` environment variable
- Used to fetch migration fix guidance from repos

## Future Enhancements

- [ ] Automatic severity trending (compare to previous scans)
- [ ] Integration with ticket systems (Jira, Linear) for tracking remediation
- [ ] Slack interactive buttons ("Re-scan", "Create ticket", "Snooze")
- [ ] Custom policy evaluation (e.g., "block deploys with CRITICAL vulns")
- [ ] Historical reporting (vulnerability trends over time)

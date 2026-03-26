package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"k8s.io/utils/ptr"
)

// CheckCredentials polls EC2 DescribeInstanceTypes until the call succeeds or
// the context is cancelled. Newly provisioned AWS credentials may take several
// seconds to propagate; this blocks the operand from starting until they are
// usable.
func CheckCredentials(ctx context.Context, region string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := ec2.NewFromConfig(cfg)

	for {
		_, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
			MaxResults: ptr.To(int32(5)),
		})
		if err == nil {
			return nil
		}
		fmt.Printf("credential check failed (retrying): %v\n", err)

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for valid credentials: %w", err)
		case <-time.After(3 * time.Second):
		}
	}
}

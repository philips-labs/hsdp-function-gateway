# hsdp-function-gateway

[HSDP function](https://registry.terraform.io/providers/philips-software/hsdp/latest/docs/guides/functions) gateway is 
a helper microservice for the `hsdp_function` resource. It implements the following:

- IronWorker service management and Task orchestration
- Sync and Async function endpoint handler
- CRON scheduler

## deploy

The gateway should be deployed using the [siderite-backend](https://github.com/philips-labs/terraform-cloudfoundry-siderite-backend) Terraform module
as it takes care of injecting the required configuration

## contact / getting help

Post your questions in the `#terraform` HSDP Slack channel or [start a discussion](https://github.com/philips-labs/hsdp-function-gateway/discussions) here

## license

License is MIT

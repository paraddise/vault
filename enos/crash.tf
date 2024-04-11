terraform {
  required_version = ">= 1.2.0"

  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
    enos = {
      source  = "registry.terraform.io/hashicorp-forge/enos"
      version = ">= 0.4.0"
    }
  }

}

provider "aws" {
  region = "us-west-2"
}

provider "enos" {
  transport = {
    ssh = {
      private_key_path = "/Users/ryan/code/hashi/vault/enos/support/private_key.pem"
      user             = "ubuntu"
    }
  }
  alias = "ubuntu"
}

module "get_local_metadata" {
  source = "./modules/get_local_metadata"
}

module "build_vault" {
  source = "./modules/build_local"

  distro        = "ubuntu"
  edition       = "ce"
  goarch        = "amd64"
  goos          = "linux"
  arch          = "amd64"
  artifact_path = "/Users/ryan/code/hashi/vault/enos/support/vault_from_artifactory.zip"
  artifact_type = "bundle"
  build_tags    = ["ui"]
}

module "ec2_info" {
  source = "./modules/ec2_info"
}

module "create_vpc" {
  source = "./modules/create_vpc"

  common_tags = {
    Environment    = "ci"
    Project        = "Enos"
    "Project Name" = "vault-enos-integration"
  }
  environment = "ci"
}

module "create_vault_cluster_targets" {
  depends_on = [module.create_vpc]
  source     = "./modules/target_ec2_instances"

  providers = {
    enos = enos.ubuntu
  }

  vpc_id      = module.create_vpc.id
  ami_id      = module.ec2_info.ami_ids["amd64"]["ubuntu"]["22.04"]
  ssh_keypair = "enos-ci-ssh-key"
  common_tags = {
    Environment    = "ci"
    Project        = "Enos"
    "Project Name" = "vault-enos-integration"
  }
  project_name    = "vault-enos-integration"
  cluster_tag_key = "Type"
}

module "create_vault_cluster" {
  depends_on = [
    module.build_vault,
    module.create_vault_cluster_targets
  ]

  source = "./modules/vault_cluster"

  providers = {
    enos = enos.ubuntu
  }

  consul_license          = null
  target_hosts            = module.create_vault_cluster_targets.hosts
  cluster_name            = module.create_vault_cluster_targets.cluster_name
  manage_service          = true
  seal_type               = "shamir"
  storage_backend         = "raft"
  enable_audit_devices    = true
  install_dir             = "/opt/vault/bin"
  log_level               = "trace"
  backend_cluster_tag_key = "VaultStorage"
  local_artifact_path     = "/Users/ryan/code/hashi/vault/enos/support/vault_from_artifactory.zip"
}

output "path" {
  value = "/Users/ryan/Downloads/terraform"
}

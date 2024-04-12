/**
 * Copyright (c) HashiCorp, Inc.
 * SPDX-License-Identifier: BUSL-1.1
 */

import Service, { inject as service } from '@ember/service';
import { keepLatestTask, task } from 'ember-concurrency';
import { tracked } from '@glimmer/tracking';
import { DEBUG } from '@glimmer/env';

export default class VersionService extends Service {
  @service store;
  @service featureFlag;
  @tracked licenseFeatures = [];
  @tracked activatedFeatures = [];
  @tracked version = null;
  @tracked type = null;

  get isEnterprise() {
    return this.type === 'enterprise';
  }

  get isCommunity() {
    return !this.isEnterprise;
  }

  get versionDisplay() {
    if (!this.version) {
      return '';
    }
    return this.isEnterprise ? `v${this.version.slice(0, this.version.indexOf('+'))}` : `v${this.version}`;
  }

  /* License Features */
  get hasPerfReplication() {
    return this.licenseFeatures.includes('Performance Replication');
  }

  get hasDRReplication() {
    return this.licenseFeatures.includes('DR Replication');
  }

  get hasSentinel() {
    return this.licenseFeatures.includes('Sentinel');
  }

  get hasNamespaces() {
    return this.licenseFeatures.includes('Namespaces');
  }

  get hasControlGroups() {
    return this.licenseFeatures.includes('Control Groups');
  }

  get hasSecretsSync() {
    return this.licenseFeatures.includes('Secrets Sync');
  }

  /* Activated Features */
  get secretsSyncIsActivated() {
    return this.activatedFeaturesFeatures.includes('secrets-sync');
  }

  @task({ drop: true })
  *getVersion() {
    if (this.version) return;
    // Fetch seal status with token to get version
    const response = yield this.store.adapterFor('cluster').sealStatus(false);
    this.version = response?.version;
  }

  @task
  *getType() {
    if (this.type !== null) return;
    const response = yield this.store.adapterFor('cluster').health();
    if (response.has_chroot_namespace) {
      // chroot_namespace feature is only available in enterprise
      this.type = 'enterprise';
      return;
    }
    this.type = response.enterprise ? 'enterprise' : 'community';
  }

  @keepLatestTask
  *getLicenseFeatures() {
    if (this.licenseFeatures?.length || this.isCommunity) {
      return;
    }
    try {
      const response = yield this.store.adapterFor('cluster').features();
      this.licenseFeatures = response.features;
      return;
    } catch (err) {
      // if we fail here, we're likely in DR Secondary mode and don't need to worry about it
    }
  }

  @keepLatestTask
  *getActivatedFeatures() {
    // Response could change between user sessions so fire off endpoint without checking if activated features are already set.
    try {
      const response = yield this.store
        .adapterFor('application')
        .ajax('/v1/sys/activation-flags', 'GET', { unauthenticated: true, namespace: null });
      this.activatedFeatures = response.data?.activated;
      return;
    } catch (error) {
      if (DEBUG) console.error(error); // eslint-disable-line no-console
      return [];
    }
  }

  fetchVersion() {
    return this.getVersion.perform();
  }

  fetchType() {
    return this.getType.perform();
  }

  fetchLicenseFeatures() {
    return this.getLicenseFeatures.perform();
  }

  fetchActivatedFeatures() {
    return this.getActivatedFeatures.perform();
  }
}

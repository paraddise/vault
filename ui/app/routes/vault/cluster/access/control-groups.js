/**
 * Copyright (c) HashiCorp, Inc.
 * SPDX-License-Identifier: BUSL-1.1
 */

import { service } from '@ember/service';
import Route from '@ember/routing/route';
import UnloadModel from 'vault/mixins/unload-model-route';

export default Route.extend(UnloadModel, {
  store: service(),
  version: service(),

  beforeModel() {
    return this.version.fetchLicenseFeatures().then(() => {
      return this._super(...arguments);
    });
  },

  model() {
    return this.version.hasControlGroups ? this.store.createRecord('control-group') : null;
  },
});

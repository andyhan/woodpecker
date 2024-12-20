import type { InjectionKey, Ref } from 'vue';
import { inject as vueInject, provide as vueProvide } from 'vue';

import type { Org, OrgPermissions, Pipeline, PipelineConfig, Repo } from '~/lib/api/types';

import type { Tab } from './useTabs';

export interface InjectKeys {
  repo: Ref<Repo>;
  org: Ref<Org | undefined>;
  'org-permissions': Ref<OrgPermissions | undefined>;
  pipeline: Ref<Pipeline | undefined>;
  'pipeline-configs': Ref<PipelineConfig[] | undefined>;
  tabs: Ref<Tab[]>;
}

export function inject<T extends keyof InjectKeys>(key: T): InjectKeys[T] {
  const value = vueInject<InjectKeys[T]>(key);
  if (value === undefined) {
    throw new Error(`Please provide a value for ${key}`);
  }
  return value;
}

export function provide<T extends keyof InjectKeys>(key: T, value: InjectKeys[T]): void {
  return vueProvide(key, value as T extends InjectionKey<infer V> ? V : InjectKeys[T]);
}

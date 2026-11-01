import { serviceId } from '../helpers'

const b64url = (value: string) => Buffer.from(value).toString('base64url')

export const clusterUrl = (account: string, region: string, cluster: string) =>
  `/clusters/${b64url(`${account}|${region}|${cluster}`)}`

export const serviceUrl = (account: string, region: string, cluster: string, service: string, tab?: string) =>
  `/services/${serviceId(account, region, cluster, service)}${tab ? `/${tab}` : ''}`

// [account, region, cluster, service] tuples of the fake-aws estate.
export const SVC = {
  paymentsApi: ['482109375512', 'us-east-1', 'core-services', 'payments-api'],
  notifications: ['482109375512', 'us-east-1', 'core-services', 'notifications'],
  imageResizer: ['482109375512', 'us-east-1', 'background-jobs', 'image-resizer'],
  jenkins: ['482109375512', 'us-east-1', 'ci-tools', 'jenkins-controller'],
  catalogApi: ['482109375512', 'eu-west-1', 'eu-apps', 'catalog-api'],
  edgeNginx: ['482109375512', 'eu-central-1', 'edge-proxy', 'edge-nginx'],
  stagingApi: ['731022849906', 'us-east-1', 'staging-web', 'api'],
} as const

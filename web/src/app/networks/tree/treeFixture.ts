// Shared fixture for the network tree tests (Tree.test.tsx, useTreeState.test.ts) and the VPC page tests.
// Shape: 2 AZs, 3 subnets (one empty), a standalone-tasks node and one unhealthy task.
import type { TreeNode } from '../../../api/networks'

export const KEYS = {
  vpc: 'vpc:vpc-0a1',
  azA: 'az:vpc-0a1:eu-west-1a',
  azB: 'az:vpc-0a1:eu-west-1b',
  subnetA: 'sn:subnet-aaa',
  subnetEmpty: 'sn:subnet-bbb',
  subnetB: 'sn:subnet-ccc',
  clusterA: 'sn:subnet-aaa:cl-core',
  payments: 'sn:subnet-aaa:cl-core:svc-pay',
  orders: 'sn:subnet-aaa:cl-core:svc-ord',
  taskHealthy: 'task:11111111aaaa',
  taskUnhealthy: 'task:22222222bbbb',
  taskOrders: 'task:33333333cccc',
  clusterCi: 'sn:subnet-ccc:cl-ci',
  standalone: 'sn:subnet-ccc:cl-ci:_standalone',
  taskStandalone: 'task:44444444dddd',
} as const

export const SERVICE_KEYS = [KEYS.payments, KEYS.orders, KEYS.standalone]

export function buildTree(): TreeNode {
  return {
    key: KEYS.vpc,
    kind: 'vpc',
    label: 'core-vpc',
    tasks: 4,
    vpc: { vpc_id: 'vpc-0a1', cidr: '10.20.0.0/16', zones: 2 },
    children: [
      {
        key: KEYS.azA,
        kind: 'az',
        label: 'eu-west-1a',
        tasks: 3,
        az: { subnets: 2 },
        children: [
          {
            key: KEYS.subnetA,
            kind: 'subnet',
            label: 'private-a',
            tasks: 3,
            subnet: { subnet_id: 'subnet-aaa', cidr: '10.20.1.0/24', public: false, ips_used: 27, ips_total: 251 },
            children: [
              {
                key: KEYS.clusterA,
                kind: 'cluster',
                label: 'core-services',
                tasks: 3,
                cluster: { cluster_id: 'cl-core', tag: { name: 'Prod', color: 'red' }, services: 2 },
                children: [
                  {
                    key: KEYS.payments,
                    kind: 'service',
                    label: 'payments-api',
                    tasks: 2,
                    service: { service_id: 'svc-pay', status: 'steady', revision: 41 },
                    children: [
                      {
                        key: KEYS.taskHealthy,
                        kind: 'task',
                        label: '11111111',
                        tasks: 1,
                        task: { task_id: '11111111aaaa', service_id: 'svc-pay', private_ip: '10.20.1.14', group: '', unhealthy: false },
                      },
                      {
                        key: KEYS.taskUnhealthy,
                        kind: 'task',
                        label: '22222222',
                        tasks: 1,
                        task: { task_id: '22222222bbbb', service_id: 'svc-pay', private_ip: '10.20.1.15', group: '', unhealthy: true },
                      },
                    ],
                  },
                  {
                    key: KEYS.orders,
                    kind: 'service',
                    label: 'orders-api',
                    tasks: 1,
                    service: { service_id: 'svc-ord', status: 'deploying', revision: 7 },
                    children: [
                      {
                        key: KEYS.taskOrders,
                        kind: 'task',
                        label: '33333333',
                        tasks: 1,
                        task: { task_id: '33333333cccc', service_id: 'svc-ord', private_ip: '10.20.1.21', group: '', unhealthy: false },
                      },
                    ],
                  },
                ],
              },
            ],
          },
          {
            key: KEYS.subnetEmpty,
            kind: 'subnet',
            label: 'public-a',
            tasks: 0,
            subnet: { subnet_id: 'subnet-bbb', cidr: '10.20.0.0/20', public: true, ips_used: 1, ips_total: 4091 },
          },
        ],
      },
      {
        key: KEYS.azB,
        kind: 'az',
        label: 'eu-west-1b',
        tasks: 1,
        az: { subnets: 1 },
        children: [
          {
            key: KEYS.subnetB,
            kind: 'subnet',
            label: 'private-b',
            tasks: 1,
            subnet: { subnet_id: 'subnet-ccc', cidr: '10.20.2.0/24', public: false, ips_used: 5, ips_total: 251 },
            children: [
              {
                key: KEYS.clusterCi,
                kind: 'cluster',
                label: 'ci-tools',
                tasks: 1,
                cluster: { cluster_id: 'cl-ci', tag: { name: '', color: '' }, services: 0 },
                children: [
                  {
                    key: KEYS.standalone,
                    kind: 'standalone',
                    label: 'Standalone tasks',
                    tasks: 1,
                    children: [
                      {
                        key: KEYS.taskStandalone,
                        kind: 'task',
                        label: '44444444',
                        tasks: 1,
                        task: { task_id: '44444444dddd', service_id: '', private_ip: '10.20.2.9', group: 'family:report-backfill', unhealthy: false },
                      },
                    ],
                  },
                ],
              },
            ],
          },
        ],
      },
    ],
  }
}

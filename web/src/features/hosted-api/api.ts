/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export interface HostedUpstream {
  id: number
  name: string
  base_url: string
  key_hint: string
  models: string
  enabled: boolean
}

export interface HostedState {
  upstreams: HostedUpstream[]
  platform_fallback: boolean
  connection_limit: number
  daily_token_limit: number
  used_tokens: number
  day: string
  tier: string
  base_path: string
}

export async function getHostedState(): Promise<HostedState> {
  const response = await api.get<{ success: boolean; data: HostedState }>(
    '/api/hosted'
  )
  return requireServerSuccess(response.data).data
}

export type HostedAction = 'save' | 'delete' | 'fallback'

export async function hostedMutationContext(
  action: HostedAction,
  id: number,
  body: string
) {
  const bytes = await crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(body)
  )
  return {
    action,
    upstream_id: id,
    digest: Array.from(new Uint8Array(bytes), (byte) =>
      byte.toString(16).padStart(2, '0')
    ).join(''),
  }
}

export async function mutateHosted(
  action: HostedAction,
  id: number,
  body: string,
  proof: string
): Promise<void> {
  let method = 'put'
  let url = '/api/hosted/settings'
  if (action !== 'fallback') {
    url = `/api/hosted/upstreams${id ? `/${id}` : ''}`
    method = id ? 'put' : 'post'
    if (action === 'delete') method = 'delete'
  }
  const response = await api.request({
    method,
    url,
    data: body,
    headers: { 'Content-Type': 'application/json', 'X-Security-Proof': proof },
  })
  requireServerSuccess(response.data)
}

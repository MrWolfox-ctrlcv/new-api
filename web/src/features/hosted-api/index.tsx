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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { PlugZap, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import {
  getHostedState,
  hostedMutationContext,
  mutateHosted,
  type HostedAction,
  type HostedUpstream,
} from './api'
import { UpstreamForm } from './components/upstream-form'
import type { UpstreamFormValues } from './schema'

const queryKey = ['hosted-api']

export function HostedAPIs() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const state = useQuery({ queryKey, queryFn: getHostedState })
  const verification = useSecureVerification()
  const [editing, setEditing] = useState<HostedUpstream | null | undefined>()
  const [deleting, setDeleting] = useState<HostedUpstream | null>(null)
  const [confirmFallback, setConfirmFallback] = useState(false)
  const mutation = useMutation({
    mutationFn: async (input: {
      action: HostedAction
      id: number
      payload?: unknown
    }) => {
      const body =
        input.payload === undefined ? '' : JSON.stringify(input.payload)
      const context = await hostedMutationContext(input.action, input.id, body)
      const proof = await verification.requestVerification({
        scope: 'hosted_api.manage',
        context,
      })
      if (!proof) return false
      await mutateHosted(input.action, input.id, body, proof.proof_token)
      return true
    },
    onSuccess: async (changed) => {
      if (!changed) return
      setEditing(undefined)
      setDeleting(null)
      setConfirmFallback(false)
      await client.invalidateQueries({ queryKey })
    },
    onError: (error) => handleServerError(error),
  })
  async function save(value: UpstreamFormValues): Promise<void> {
    // Mutation errors are presented once by onError; keep the form for correction.
    await mutation
      .mutateAsync({
        action: 'save',
        id: editing?.id ?? 0,
        payload: {
          ...value,
          models: [
            ...new Set(
              value.models
                .split(/[\n,]/)
                .map((name) => name.trim())
                .filter(Boolean)
            ),
          ],
        },
      })
      .catch(() => undefined)
  }
  const busy = mutation.isPending || verification.isActive
  const data = state.data
  const base = window.location.origin + (data?.base_path ?? '/v1/hosted')
  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('My APIs')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button
            className='min-h-11'
            disabled={
              !data || busy || data.upstreams.length >= data.connection_limit
            }
            onClick={() => setEditing(null)}
          >
            <Plus aria-hidden='true' />
            {t('Connect an upstream')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div
            className='mx-auto grid w-full max-w-5xl min-w-0 gap-4 pb-6'
            data-hosted-api
          >
            {state.isPending && <LoadingState />}
            {state.isError && (
              <ErrorState
                onRetry={() => {
                  void state.refetch()
                }}
              />
            )}
            {data && (
              <>
                <Card>
                  <CardHeader>
                    <div className='flex flex-wrap items-center justify-between gap-2'>
                      <CardTitle>{t('Private API aggregation')}</CardTitle>
                      <Badge variant='secondary'>
                        {data.tier === 'vip' ? 'VIP' : t('Free tier')}
                      </Badge>
                    </div>
                    <CardDescription>
                      {t(
                        'Use your own upstream credentials through one platform API key. Your private upstream calls do not spend your platform balance.'
                      )}
                    </CardDescription>
                  </CardHeader>
                  <CardContent className='grid min-w-0 gap-4 sm:grid-cols-2'>
                    <div className='rounded-lg border p-4'>
                      <p className='text-muted-foreground text-sm'>
                        {t('Connected upstreams')}
                      </p>
                      <p className='mt-1 text-2xl font-semibold'>
                        {data.upstreams.length} / {data.connection_limit}
                      </p>
                      <p className='text-muted-foreground mt-2 text-xs'>
                        {t(
                          'One Base URL and key count as one upstream, regardless of model count. Disabled connections still occupy a slot.'
                        )}
                      </p>
                    </div>
                    <div className='rounded-lg border p-4'>
                      <p className='text-muted-foreground text-sm'>
                        {t('Daily tokens')}
                      </p>
                      <p className='mt-1 text-xl font-semibold break-words'>
                        {formatNumber(data.used_tokens, locale)} /{' '}
                        {formatNumber(data.daily_token_limit, locale)}
                      </p>
                      <p className='text-muted-foreground mt-2 text-xs'>
                        {t(
                          'Input plus output; cached input counted once. Resets at 00:00 Beijing time. In-flight requests reserve their maximum usage.'
                        )}
                      </p>
                    </div>
                    <p className='text-muted-foreground text-xs sm:col-span-2'>
                      {t(
                        'Free: 1 upstream and 10M tokens/day. VIP: 5 upstreams and 100M tokens/day. If your tier changes, only the earliest connections within your new limit are used.'
                      )}
                    </p>
                  </CardContent>
                </Card>
                <Card>
                  <CardHeader>
                    <CardTitle>{t('Client connection')}</CardTitle>
                    <CardDescription>
                      {t(
                        'Set this Base URL in your chat client, use an API key from API Keys, and select one of your configured model IDs.'
                      )}
                    </CardDescription>
                  </CardHeader>
                  <CardContent className='grid min-w-0 gap-3'>
                    <div className='flex min-w-0 items-center gap-2 rounded-lg border p-3'>
                      <code className='min-w-0 flex-1 text-sm break-all'>
                        {base}
                      </code>
                      <CopyButton value={base} />
                    </div>
                    <Button
                      variant='outline'
                      className='min-h-11 w-fit'
                      render={<Link to='/keys' />}
                    >
                      {t('API Keys')}
                    </Button>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Text chat and streaming are supported. Maximum output: 32K tokens per request. Missing usage or interrupted responses keep the reserved tokens to prevent bypassing the daily limit.'
                      )}
                    </p>
                  </CardContent>
                </Card>
                <Card>
                  <CardHeader>
                    <div className='flex items-start justify-between gap-4'>
                      <div className='min-w-0'>
                        <CardTitle>
                          <label htmlFor='platform-fallback'>
                            {t('Paid platform fallback')}
                          </label>
                        </CardTitle>
                        <CardDescription className='mt-2'>
                          {t(
                            'Off by default. When enabled, unavailable private upstreams or an exhausted daily allowance may use platform channels and spend your platform balance at the API key group price.'
                          )}
                        </CardDescription>
                      </div>
                      <Switch
                        id='platform-fallback'
                        checked={data.platform_fallback}
                        disabled={busy}
                        onCheckedChange={(enabled) => {
                          if (enabled) setConfirmFallback(true)
                          else {
                            mutation.mutate({
                              action: 'fallback',
                              id: 0,
                              payload: { platform_fallback: false },
                            })
                          }
                        }}
                      />
                    </div>
                  </CardHeader>
                </Card>
                {!data.upstreams.length && (
                  <EmptyState
                    icon={PlugZap}
                    title={t('No private upstreams yet')}
                    description={t(
                      'Connect your first API to start aggregating your own models.'
                    )}
                  />
                )}
                {data.upstreams.map((upstream, index) => (
                  <Card key={upstream.id}>
                    <CardHeader>
                      <div className='flex flex-wrap items-start justify-between gap-2'>
                        <CardTitle className='min-w-0 break-all'>
                          {upstream.name}
                        </CardTitle>
                        <Badge variant='outline'>
                          {t(
                            upstream.enabled && index < data.connection_limit
                              ? 'Enabled'
                              : 'Inactive'
                          )}
                        </Badge>
                      </div>
                      <CardDescription className='break-all'>
                        {upstream.base_url}
                      </CardDescription>
                    </CardHeader>
                    <CardContent className='grid min-w-0 gap-3'>
                      <p className='text-muted-foreground text-sm'>
                        {t('Upstream API key')}: ••••{upstream.key_hint}
                      </p>
                      <div className='flex min-w-0 flex-wrap gap-2'>
                        {(JSON.parse(upstream.models) as string[]).map(
                          (name) => (
                            <Badge
                              key={name}
                              variant='secondary'
                              className='max-w-full break-all whitespace-normal'
                            >
                              {name}
                            </Badge>
                          )
                        )}
                      </div>
                      <div className='grid grid-cols-2 gap-2 sm:flex'>
                        <Button
                          variant='outline'
                          className='min-h-11'
                          disabled={busy}
                          onClick={() => setEditing(upstream)}
                        >
                          {t('Edit')}
                        </Button>
                        <Button
                          variant='outline'
                          className='min-h-11'
                          disabled={busy}
                          onClick={() => setDeleting(upstream)}
                        >
                          {t('Delete')}
                        </Button>
                      </div>
                    </CardContent>
                  </Card>
                ))}
              </>
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {editing !== undefined && (
        <UpstreamForm
          key={editing?.id ?? 'new'}
          upstream={editing}
          busy={busy}
          onClose={() => setEditing(undefined)}
          onSave={save}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open && !busy) setDeleting(null)
        }}
        title={t('Delete upstream?')}
        desc={t(
          'The stored upstream key will be deleted. Existing daily usage will remain.'
        )}
        destructive
        isLoading={busy}
        handleConfirm={() => {
          if (deleting) mutation.mutate({ action: 'delete', id: deleting.id })
        }}
      />
      <ConfirmDialog
        open={confirmFallback}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirmFallback(false)
        }}
        title={t('Enable paid platform fallback?')}
        desc={t(
          'Platform fallback spends your platform balance using your API key group pricing. It only starts before a private response begins.'
        )}
        isLoading={busy}
        handleConfirm={() =>
          mutation.mutate({
            action: 'fallback',
            id: 0,
            payload: { platform_fallback: true },
          })
        }
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}

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
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import type { HostedUpstream } from '../api'
import { upstreamSchema, type UpstreamFormValues } from '../schema'

export function UpstreamForm(props: {
  upstream: HostedUpstream | null
  busy: boolean
  onClose: () => void
  onSave: (value: UpstreamFormValues) => Promise<void>
}) {
  const { t } = useTranslation()
  const form = useForm<UpstreamFormValues>({
    resolver: zodResolver(upstreamSchema),
    defaultValues: {
      name: props.upstream?.name ?? '',
      base_url: props.upstream?.base_url ?? '',
      key: '',
      models: props.upstream
        ? (JSON.parse(props.upstream.models) as string[]).join('\n')
        : '',
      enabled: props.upstream?.enabled ?? true,
    },
  })
  const disabled = props.busy || form.formState.isSubmitting
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !disabled) props.onClose()
      }}
      title={t(props.upstream ? 'Edit upstream' : 'Connect an upstream')}
      description={t(
        'OpenAI-compatible text chat, including streaming. Enter the Base URL ending in /v1 when required by your provider.'
      )}
      footer={
        <>
          <Button
            className='min-h-11'
            variant='outline'
            disabled={disabled}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button
            className='min-h-11'
            type='submit'
            form='hosted-upstream-form'
            disabled={disabled}
          >
            {t('Save')}
          </Button>
        </>
      }
    >
      <form
        id='hosted-upstream-form'
        className='px-1'
        onSubmit={form.handleSubmit(async (value) => {
          if (!props.upstream && !value.key) {
            form.setError('key', { message: 'Enter the upstream API key' })
            return
          }
          await props.onSave(value)
        })}
      >
        <FieldGroup>
          <Field data-invalid={!!form.formState.errors.name}>
            <FieldLabel htmlFor='hosted-name'>{t('Name')}</FieldLabel>
            <Input
              id='hosted-name'
              className='min-h-11 text-base'
              autoComplete='off'
              disabled={disabled}
              aria-invalid={!!form.formState.errors.name}
              {...form.register('name')}
            />
            <FieldError>
              {form.formState.errors.name?.message &&
                t(form.formState.errors.name.message)}
            </FieldError>
          </Field>
          <Field data-invalid={!!form.formState.errors.base_url}>
            <FieldLabel htmlFor='hosted-base'>
              {t('Upstream Base URL')}
            </FieldLabel>
            <Input
              id='hosted-base'
              className='min-h-11 text-base'
              type='url'
              placeholder='https://api.example.com/v1'
              disabled={disabled}
              aria-invalid={!!form.formState.errors.base_url}
              {...form.register('base_url')}
            />
            <FieldError>
              {form.formState.errors.base_url?.message &&
                t(form.formState.errors.base_url.message)}
            </FieldError>
          </Field>
          <Field data-invalid={!!form.formState.errors.key}>
            <FieldLabel htmlFor='hosted-secret'>
              {t('Upstream API key')}
            </FieldLabel>
            <Input
              id='hosted-secret'
              className='min-h-11 text-base'
              type='password'
              autoComplete='new-password'
              placeholder={
                props.upstream ? t('Leave blank to keep the current key') : ''
              }
              disabled={disabled}
              aria-invalid={!!form.formState.errors.key}
              {...form.register('key')}
            />
            <FieldError>
              {form.formState.errors.key?.message &&
                t(form.formState.errors.key.message)}
            </FieldError>
          </Field>
          <Field data-invalid={!!form.formState.errors.models}>
            <FieldLabel htmlFor='hosted-models'>
              {t('Model IDs, one per line')}
            </FieldLabel>
            <Textarea
              id='hosted-models'
              className='min-h-28'
              disabled={disabled}
              aria-invalid={!!form.formState.errors.models}
              {...form.register('models')}
            />
            <FieldError>
              {form.formState.errors.models?.message &&
                t(form.formState.errors.models.message)}
            </FieldError>
          </Field>
          <Field orientation='horizontal'>
            <FieldLabel htmlFor='hosted-enabled'>{t('Enabled')}</FieldLabel>
            <Switch
              id='hosted-enabled'
              checked={form.watch('enabled')}
              onCheckedChange={(value) => form.setValue('enabled', value)}
              disabled={disabled}
            />
          </Field>
        </FieldGroup>
      </form>
    </Dialog>
  )
}

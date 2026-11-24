import React, { useState, useEffect, useCallback } from 'react';
import {
  KeyRound,
  Plus,
  Trash2,
  Shield,
  RotateCcw,
  Eye,
  EyeOff,
  AlertCircle,
  CheckCircle2,
  Lock,
} from 'lucide-react';
import {
  fetchSecrets,
  createSecret,
  deleteSecret,
  type SecretMetadata,
} from '@/lib/api';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn, formatRelativeTime } from '@/lib/utils';

export interface SecretsViewProps {
  secrets?: SecretMetadata[];
  onRefresh?: () => void;
  isLoading?: boolean;
}

export function SecretsView({
  secrets: initialSecrets = [],
  onRefresh,
  isLoading = false,
}: SecretsViewProps) {
  const [secretsList, setSecretsList] = useState<SecretMetadata[]>(initialSecrets);
  const [loading, setLoading] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);

  // Add Secret Dialog State
  const [isAddOpen, setIsAddOpen] = useState(false);
  const [newKey, setNewKey] = useState('');
  const [newValue, setNewValue] = useState('');
  const [newScope, setNewScope] = useState<'global' | 'repo'>('global');
  const [newRepo, setNewRepo] = useState('');
  const [showValue, setShowValue] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  // Delete Confirmation State
  const [deleteTargetKey, setDeleteTargetKey] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const loadData = useCallback(async () => {
    try {
      setLoading(true);
      const data = await fetchSecrets();
      setSecretsList(data || []);
      setErrorMessage(null);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to fetch secrets';
      setErrorMessage(msg);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const handleRefresh = () => {
    onRefresh?.();
    loadData();
  };

  const handleAddSecret = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newKey.trim() || !newValue.trim()) {
      setErrorMessage('Secret key and value are required.');
      return;
    }

    try {
      setSubmitting(true);
      setErrorMessage(null);
      const scopeVal = newScope === 'repo' ? (newRepo.trim() ? `repo:${newRepo.trim()}` : 'repo') : 'global';
      await createSecret(newKey.trim(), newValue.trim(), scopeVal);

      setSuccessMessage(`Secret "${newKey.trim()}" securely stored.`);
      setTimeout(() => setSuccessMessage(null), 3500);

      // Reset form
      setNewKey('');
      setNewValue('');
      setNewScope('global');
      setNewRepo('');
      setShowValue(false);
      setIsAddOpen(false);

      await loadData();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to create secret';
      setErrorMessage(msg);
    } finally {
      setSubmitting(false);
    }
  };

  const handleDeleteSecret = async () => {
    if (!deleteTargetKey) return;
    try {
      setDeleting(true);
      setErrorMessage(null);
      await deleteSecret(deleteTargetKey);

      setSuccessMessage(`Secret "${deleteTargetKey}" removed.`);
      setTimeout(() => setSuccessMessage(null), 3500);

      setDeleteTargetKey(null);
      await loadData();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to delete secret';
      setErrorMessage(msg);
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div className="space-y-5">
      {/* Header Bar */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-5 flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-2.5">
            <Shield className="w-5 h-5 text-geist-blue" />
            <h2 className="text-base font-semibold text-white tracking-tight">Secrets Vault</h2>
            <Badge variant="outline" className="font-mono text-xs">
              {secretsList.length}
            </Badge>
          </div>
          <p className="text-xs text-geist-secondary mt-1">
            Encrypted environment variables and credentials injected into workflow runner jobs at runtime.
          </p>
        </div>

        <div className="flex items-center gap-2.5">
          <Button
            variant="outline"
            size="sm"
            onClick={handleRefresh}
            disabled={loading || isLoading}
            className="h-8 gap-1.5 text-xs text-geist-secondary hover:text-white"
          >
            <RotateCcw className={cn('w-3.5 h-3.5', (loading || isLoading) && 'animate-spin')} />
            <span>Refresh</span>
          </Button>

          <Button
            variant="default"
            size="sm"
            onClick={() => {
              setErrorMessage(null);
              setIsAddOpen(true);
            }}
            className="h-8 gap-1.5 text-xs font-medium"
          >
            <Plus className="w-3.5 h-3.5" />
            <span>Add Secret</span>
          </Button>
        </div>
      </div>

      {/* Notifications */}
      {errorMessage && (
        <div className="p-3 rounded-geist border border-status-danger-border bg-status-danger-bg text-status-danger-text text-xs flex items-center justify-between gap-2 animate-in fade-in duration-150">
          <div className="flex items-center gap-2">
            <AlertCircle className="w-4 h-4 flex-shrink-0" />
            <span>{errorMessage}</span>
          </div>
          <button
            onClick={() => setErrorMessage(null)}
            className="text-status-danger-text hover:underline text-[11px]"
          >
            Dismiss
          </button>
        </div>
      )}

      {successMessage && (
        <div className="p-3 rounded-geist border border-status-success-border bg-status-success-bg text-status-success-text text-xs flex items-center gap-2 animate-in fade-in duration-150">
          <CheckCircle2 className="w-4 h-4 flex-shrink-0" />
          <span>{successMessage}</span>
        </div>
      )}

      {/* Secrets Table */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
        {secretsList.length === 0 ? (
          <div className="p-12 text-center text-geist-secondary text-xs space-y-3">
            <Lock className="w-10 h-10 mx-auto text-geist-hairline" />
            <div className="space-y-1">
              <p className="text-sm font-semibold text-white">No Secrets in Vault</p>
              <p className="text-geist-faint max-w-sm mx-auto">
                No encrypted secrets have been added yet. Add tokens, API keys, or deploy passwords to make them available in runner jobs.
              </p>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setIsAddOpen(true)}
              className="h-8 text-xs mt-2"
            >
              Add First Secret
            </Button>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className="border-b border-geist-hairline bg-geist-elevated/40 text-[11px] text-geist-faint font-mono uppercase tracking-wider">
                  <th className="py-2.5 px-4 font-medium">Secret Key</th>
                  <th className="py-2.5 px-4 font-medium">Scope</th>
                  <th className="py-2.5 px-4 font-medium">Provider</th>
                  <th className="py-2.5 px-4 font-medium">Encryption</th>
                  <th className="py-2.5 px-4 font-medium">Updated</th>
                  <th className="py-2.5 px-4 font-medium text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-geist-hairline font-mono">
                {secretsList.map((sec) => (
                  <tr
                    key={sec.key}
                    className="hover:bg-geist-hover transition-colors duration-100 group"
                  >
                    <td className="py-3 px-4 whitespace-nowrap">
                      <div className="flex items-center gap-2 font-semibold text-white">
                        <KeyRound className="w-3.5 h-3.5 text-geist-blue flex-shrink-0" />
                        <span>{sec.key}</span>
                      </div>
                    </td>

                    <td className="py-3 px-4 whitespace-nowrap">
                      <span
                        className={cn(
                          'px-2 py-0.5 rounded text-[11px] font-mono border',
                          sec.scope?.startsWith('repo:')
                            ? 'bg-geist-blue/15 text-geist-blueText border-geist-blue/30'
                            : 'bg-geist-elevated text-geist-secondary border-geist-hairline'
                        )}
                      >
                        {sec.scope || 'global'}
                      </span>
                    </td>

                    <td className="py-3 px-4 whitespace-nowrap text-geist-secondary">
                      <span className="px-2 py-0.5 rounded bg-geist-elevated text-geist-secondary border border-geist-hairline text-[11px]">
                        {sec.provider || 'local-vault'}
                      </span>
                    </td>

                    <td className="py-3 px-4 whitespace-nowrap">
                      <Badge variant="outline" className="font-mono text-[11px] px-2 py-0.5 text-geist-faint">
                        AES-256-GCM
                      </Badge>
                    </td>

                    <td className="py-3 px-4 whitespace-nowrap text-geist-faint text-[11px]">
                      {sec.updatedAt ? formatRelativeTime(sec.updatedAt) : 'configured'}
                    </td>

                    <td className="py-3 px-4 whitespace-nowrap text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setDeleteTargetKey(sec.key)}
                        className="h-8 w-8 p-0 text-geist-faint hover:text-status-danger-text hover:bg-status-danger-bg"
                        title={`Delete ${sec.key}`}
                        aria-label={`Delete secret ${sec.key}`}
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Add Secret Modal Dialog */}
      {isAddOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/75 backdrop-blur-sm animate-in fade-in duration-150">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="add-secret-title"
            className="w-full max-w-md rounded-geist bg-geist-surface border border-geist-hairline shadow-2xl p-6 space-y-4 animate-in zoom-in-95 duration-150"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between pb-3 border-b border-geist-hairline">
              <div className="flex items-center gap-2">
                <Shield className="w-4 h-4 text-geist-blue" />
                <h3 id="add-secret-title" className="text-sm font-semibold text-white">
                  Add Encrypted Secret
                </h3>
              </div>
              <button
                type="button"
                onClick={() => setIsAddOpen(false)}
                className="text-geist-faint hover:text-white text-xs font-mono p-1"
                aria-label="Close dialog"
              >
                ✕
              </button>
            </div>

            <form onSubmit={handleAddSecret} className="space-y-4">
              {/* Secret Key */}
              <div className="space-y-1">
                <label htmlFor="secret-key-input" className="text-xs font-medium text-geist-secondary block">
                  Key Name <span className="text-status-danger-text">*</span>
                </label>
                <input
                  id="secret-key-input"
                  type="text"
                  required
                  value={newKey}
                  onChange={(e) => setNewKey(e.target.value)}
                  placeholder="e.g. GITHUB_TOKEN or AWS_SECRET_ACCESS_KEY"
                  className="w-full h-8 px-3 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white placeholder-geist-faint focus:outline-none focus:border-geist-blue transition-colors font-mono"
                />
                <p className="text-[10px] text-geist-faint">Uppercase alphanumeric with underscores.</p>
              </div>

              {/* Secret Value */}
              <div className="space-y-1">
                <label htmlFor="secret-value-input" className="text-xs font-medium text-geist-secondary block">
                  Secret Value <span className="text-status-danger-text">*</span>
                </label>
                <div className="relative">
                  <input
                    id="secret-value-input"
                    type={showValue ? 'text' : 'password'}
                    required
                    value={newValue}
                    onChange={(e) => setNewValue(e.target.value)}
                    placeholder="Enter confidential secret value..."
                    className="w-full h-8 pl-3 pr-8 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white placeholder-geist-faint focus:outline-none focus:border-geist-blue transition-colors font-mono"
                  />
                  <button
                    type="button"
                    onClick={() => setShowValue((prev) => !prev)}
                    className="absolute right-2.5 top-1/2 -translate-y-1/2 text-geist-faint hover:text-white"
                    aria-label="Toggle secret visibility"
                  >
                    {showValue ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                  </button>
                </div>
              </div>

              {/* Scope Selector */}
              <div className="space-y-1">
                <label className="text-xs font-medium text-geist-secondary block">Scope</label>
                <div className="grid grid-cols-2 gap-2">
                  <button
                    type="button"
                    onClick={() => setNewScope('global')}
                    className={cn(
                      'px-3 py-1.5 rounded-geist text-xs font-medium border text-center transition-colors',
                      newScope === 'global'
                        ? 'bg-geist-elevated text-white border-geist-hairline shadow-sm'
                        : 'border-transparent text-geist-secondary hover:text-white'
                    )}
                  >
                    Global (All Runners)
                  </button>
                  <button
                    type="button"
                    onClick={() => setNewScope('repo')}
                    className={cn(
                      'px-3 py-1.5 rounded-geist text-xs font-medium border text-center transition-colors',
                      newScope === 'repo'
                        ? 'bg-geist-elevated text-white border-geist-hairline shadow-sm'
                        : 'border-transparent text-geist-secondary hover:text-white'
                    )}
                  >
                    Repository Specific
                  </button>
                </div>
              </div>

              {newScope === 'repo' && (
                <div className="space-y-1 animate-in fade-in duration-100">
                  <label htmlFor="secret-repo-input" className="text-xs font-medium text-geist-secondary block">
                    Target Repository
                  </label>
                  <input
                    id="secret-repo-input"
                    type="text"
                    value={newRepo}
                    onChange={(e) => setNewRepo(e.target.value)}
                    placeholder="e.g. owner/repo-name"
                    className="w-full h-8 px-3 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white placeholder-geist-faint focus:outline-none focus:border-geist-blue transition-colors font-mono"
                  />
                </div>
              )}

              {/* Action Buttons */}
              <div className="flex items-center justify-end gap-2 pt-3 border-t border-geist-hairline">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setIsAddOpen(false)}
                  disabled={submitting}
                  className="h-8 text-xs"
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="default"
                  size="sm"
                  disabled={submitting}
                  className="h-8 text-xs"
                >
                  {submitting ? 'Saving...' : 'Save Secret'}
                </Button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Delete Confirmation Modal Dialog */}
      {deleteTargetKey && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/75 backdrop-blur-sm animate-in fade-in duration-150">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="delete-secret-title"
            className="w-full max-w-sm rounded-geist bg-geist-surface border border-status-danger-border shadow-2xl p-5 space-y-4 animate-in zoom-in-95 duration-150"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center gap-2.5 text-status-danger-text">
              <AlertCircle className="w-5 h-5 flex-shrink-0" />
              <h3 id="delete-secret-title" className="text-sm font-semibold">Delete Secret</h3>
            </div>

            <p className="text-xs text-geist-secondary leading-relaxed">
              Are you sure you want to permanently delete secret{' '}
              <span className="font-mono text-white font-semibold">{deleteTargetKey}</span>?
              Workflow runs requiring this secret may fail.
            </p>

            <div className="flex items-center justify-end gap-2 pt-2 border-t border-geist-hairline">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setDeleteTargetKey(null)}
                disabled={deleting}
                className="h-8 text-xs"
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={handleDeleteSecret}
                disabled={deleting}
                className="h-8 text-xs"
              >
                {deleting ? 'Deleting...' : 'Delete Secret'}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

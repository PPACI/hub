import classnames from 'classnames';
import isNull from 'lodash/isNull';
import isUndefined from 'lodash/isUndefined';
import React, { useContext, useEffect, useRef, useState } from 'react';
import { FaUser } from 'react-icons/fa';
import { MdBusiness, MdClose } from 'react-icons/md';
import { RiArrowLeftRightLine } from 'react-icons/ri';

import API from '../../../api';
import { AppCtx } from '../../../context/AppCtx';
import { ErrorKind, Organization, Repository } from '../../../types';
import compoundErrorMessage from '../../../utils/compoundErrorMessage';
import getMetaTag from '../../../utils/getMetaTag';
import ExternalLink from '../../common/ExternalLink';
import Modal from '../../common/Modal';
import RepositoryIcon from '../../common/RepositoryIcon';
import SearchRepositories from '../../common/SearchRepositories';
import styles from './ClaimOwnershipModal.module.css';

interface Props {
  open: boolean;
  onClose: () => void;
  onAuthError: () => void;
  onSuccess?: () => void;
}

const ClaimRepositoryOwnerShipModal = (props: Props) => {
  const { ctx } = useContext(AppCtx);
  const siteName = getMetaTag('siteName');
  const form = useRef<HTMLFormElement>(null);
  const [isFetchingOrgs, setIsFetchingOrgs] = useState(false);
  const [isSending, setIsSending] = useState(false);
  const [isValidated, setIsValidated] = useState(false);
  const [apiError, setApiError] = useState<string | null>(null);
  const [apiOrgsError, setApiOrgsError] = useState<string | null>(null);
  const [apiReposError, setApiReposError] = useState<string | null>(null);
  const organizationName = ctx.prefs.controlPanel.selectedOrg;
  const [selectedClaimOption, setSelectedClaimOption] = useState<'org' | 'user'>(
    !isUndefined(organizationName) ? 'org' : 'user'
  );
  const [claimingOrg, setClaimingOrg] = useState<string>(organizationName || '');
  const [organizations, setOrganizations] = useState<Organization[] | undefined>(undefined);
  const [repoItem, setRepoItem] = useState<Repository | null>(null);

  const handleOrgChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    setClaimingOrg(event.target.value);
    setSelectedClaimOption(event.target.value === '' ? 'user' : 'org');
  };

  const handleClaimingFromOpt = (type: 'user' | 'org') => {
    if (type === 'user') {
      setClaimingOrg('');
    }
    setSelectedClaimOption(type);
  };

  // Clean API error when form is focused after validation
  const cleanApiError = () => {
    if (!isNull(apiError)) {
      setApiError(null);
      setApiReposError(null);
      setApiOrgsError(null);
    }
  };

  const onCloseModal = () => {
    props.onClose();
  };

  const onRepoSelect = (repo: Repository): void => {
    setRepoItem(repo);
  };

  async function claimRepository() {
    try {
      await API.claimRepositoryOwnership(repoItem!, claimingOrg || undefined);
      if (!isUndefined(props.onSuccess)) {
        props.onSuccess();
      }
      setIsSending(false);
      onCloseModal();
    } catch (err) {
      setIsSending(false);
      if (err.kind !== ErrorKind.Unauthorized) {
        let error = compoundErrorMessage(err, 'An error occurred claiming the repository');
        if (err.kind === ErrorKind.Forbidden) {
          error =
            'You do not have permissions to claim this repository ownership. Please make sure your metadata file has been setup correctly.';
        }
        setApiError(error);
      } else {
        props.onAuthError();
      }
    }
  }

  const submitForm = () => {
    cleanApiError();
    setIsSending(true);
    if (form.current && validateForm(form.current)) {
      claimRepository();
    } else {
      setIsSending(false);
    }
  };

  const validateForm = (form: HTMLFormElement): boolean => {
    setIsValidated(true);
    return form.checkValidity();
  };

  const getOrgsNames = (): string[] => {
    if (organizations) {
      return organizations.map((org: Organization) => org.name);
    }
    return [];
  };

  useEffect(() => {
    async function fetchOrganizations() {
      try {
        setIsFetchingOrgs(true);
        const orgs = await API.getAllUserOrganizations();
        const confirmedOrganizations = orgs.filter((org: Organization) => org.confirmed);
        setOrganizations(confirmedOrganizations);
        setApiOrgsError(null);
        setIsFetchingOrgs(false);
      } catch (err) {
        setIsFetchingOrgs(false);
        if (err.kind !== ErrorKind.Unauthorized) {
          setOrganizations([]);
          setApiOrgsError('An error occurred getting your organizations, please try again later.');
        } else {
          props.onAuthError();
        }
      }
    }

    fetchOrganizations();
  }, [organizationName, props]);

  return (
    <Modal
      header={<div className={`h3 m-2 flex-grow-1 ${styles.title}`}>Claim repository ownership</div>}
      open={props.open}
      modalClassName={styles.modal}
      size="xl"
      closeButton={
        <button
          className="btn btn-sm btn-outline-secondary"
          type="button"
          disabled={isSending || isNull(repoItem)}
          onClick={submitForm}
          aria-label="Claim ownership"
        >
          {isSending ? (
            <>
              <span className="spinner-grow spinner-grow-sm" role="status" aria-hidden="true" />
              <span className="ml-2">Claiming ownership...</span>
            </>
          ) : (
            <div className="text-uppercase d-flex flex-row align-items-center">
              <RiArrowLeftRightLine className="mr-2" />
              <div>Claim ownership</div>
            </div>
          )}
        </button>
      }
      onClose={onCloseModal}
      error={apiOrgsError || apiReposError || apiError}
      cleanError={cleanApiError}
      noScrollable
    >
      <div className="w-100">
        <div className="mt-4">
          <p>
            Before claiming a repository ownership, we need to verify that you actually own it. To prove that, you need
            to add a{' '}
            <ExternalLink
              href="https://github.com/artifacthub/hub/blob/master/docs/metadata/artifacthub-repo.yml"
              className="text-reset"
              label="Open documentation"
            >
              <u>metadata file</u>
            </ExternalLink>{' '}
            to your repository and include yourself (or the person who will do the request) as an owner. This will be
            checked during the ownership claim process. Please make sure the email used in the metatata file matches
            with the one you use in {siteName}.
          </p>
        </div>
        <form
          data-testid="claimRepoForm"
          ref={form}
          className={classnames('w-100', { 'needs-validation': !isValidated }, { 'was-validated': isValidated })}
          onFocus={cleanApiError}
          autoComplete="on"
          noValidate
        >
          <div>
            <div className="d-flex flex-column my-3">
              <label className={`font-weight-bold ${styles.label}`} htmlFor="description">
                Repository:
              </label>

              {!isNull(repoItem) ? (
                <div
                  data-testid="activeRepoItem"
                  className={`border border-secondary w-100 rounded mt-1 ${styles.repoWrapper}`}
                >
                  <div className="d-flex flex-row flex-nowrap align-items-stretch justify-content-between">
                    <div className="flex-grow-1 text-truncate py-2">
                      <div className="d-flex flex-row align-items-center h-100 text-truncate">
                        <div className="d-none d-md-inline">
                          <RepositoryIcon kind={repoItem.kind} className={`mx-3 ${styles.icon}`} />
                        </div>

                        <div className="ml-2 font-weight-bold mb-0 text-truncate text-muted">
                          <span className="text-dark">{repoItem.name}</span>{' '}
                          <small className="text-muted">({repoItem.url})</small>
                        </div>

                        <div className="px-2 ml-auto w-50 text-dark text-truncate">
                          <small className="d-flex flex-row align-items-baseline ml-2">
                            (
                            <small className={`d-none d-md-inline text-uppercase text-muted mr-1 ${styles.legend}`}>
                              Publisher:{' '}
                            </small>
                            <div className={`mr-1 ${styles.tinyIcon}`}>
                              {repoItem.userAlias ? <FaUser /> : <MdBusiness />}
                            </div>
                            <span>
                              {repoItem.userAlias || repoItem.organizationDisplayName || repoItem.organizationName})
                            </span>
                          </small>
                        </div>
                      </div>
                    </div>

                    <div>
                      <button
                        className={`btn h-100 rounded-0 ${styles.closeButton}`}
                        onClick={() => setRepoItem(null)}
                        aria-label="Close"
                      >
                        <MdClose />
                      </button>
                    </div>
                  </div>
                </div>
              ) : (
                <div className={`mt-2 ${styles.searchWrapper}`}>
                  <SearchRepositories
                    label="claim-repo-ownership"
                    disabledRepositories={{
                      users: ctx.user ? [ctx.user.alias] : [],
                      organizations: getOrgsNames(),
                    }}
                    onSelection={onRepoSelect}
                    onAuthError={props.onAuthError}
                    visibleUrl
                  />
                </div>
              )}
            </div>

            <label id="claiming" className={`font-weight-bold ${styles.label}`}>
              Transfer to:
            </label>
            <div className="custom-control custom-radio mb-2">
              <input
                aria-labelledby="claiming user"
                className="custom-control-input"
                type="radio"
                name="claim"
                id="user"
                value="user"
                checked={selectedClaimOption === 'user'}
                onChange={() => handleClaimingFromOpt('user')}
                required
              />
              <label id="user" className={`custom-control-label ${styles.label}`} htmlFor="user">
                My user
              </label>
            </div>

            <div className="custom-control custom-radio mb-3">
              <input
                aria-labelledby="claiming org"
                className="custom-control-input"
                type="radio"
                name="claim"
                id="org"
                value="org"
                checked={selectedClaimOption === 'org'}
                onChange={() => handleClaimingFromOpt('org')}
                required
              />
              <label id="org" className={`custom-control-label ${styles.label}`} htmlFor="org">
                Organization
              </label>
            </div>
          </div>

          <div className="d-flex flex-row align-items-center position-relative mb-3">
            <div className="form-group w-75 mb-2">
              <select
                className="custom-select"
                aria-label="org-select"
                value={claimingOrg}
                onChange={handleOrgChange}
                required={selectedClaimOption === 'org'}
              >
                {!isUndefined(organizations) && (
                  <>
                    <option value="">Select organization</option>
                    {organizations.map((org: Organization) => (
                      <option key={`opt_${org.name}`} value={org.name}>
                        {org.name}
                      </option>
                    ))}
                  </>
                )}
              </select>
              <div className={`invalid-feedback ${styles.fieldFeedback}`}>This field is required</div>
            </div>
            {isFetchingOrgs && (
              <div className="d-inline ml-3">
                <span className="spinner-border spinner-border-sm text-primary" />
              </div>
            )}
          </div>

          <small className="text-muted text-break mt-3">
            <p>It may take a few minutes for this change to be visible across the Hub.</p>
          </small>
        </form>
      </div>
    </Modal>
  );
};

export default ClaimRepositoryOwnerShipModal;

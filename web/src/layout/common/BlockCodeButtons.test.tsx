import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';

import BlockCodeButtons from './BlockCodeButtons';

const defaultProps = {
  filename: 'name',
  content: 'this is a sample',
};

const createObjectMock = jest.fn();

Object.defineProperty(window, 'URL', {
  value: {
    createObjectURL: createObjectMock,
  },
});

describe('BlockCodeButtons', () => {
  beforeEach(() => {
    jest.resetAllMocks();
  });

  it('creates snapshot', () => {
    const { asFragment } = render(<BlockCodeButtons {...defaultProps} />);
    expect(asFragment()).toMatchSnapshot();
  });

  it('renders component', () => {
    render(<BlockCodeButtons {...defaultProps} />);

    expect(screen.getByRole('button', { name: 'Copy to clipboard' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument();
  });

  it('download file', () => {
    render(<BlockCodeButtons {...defaultProps} />);

    userEvent.click(screen.getByRole('button', { name: 'Download' }));

    const blob = new Blob([defaultProps.content], {
      type: 'text/yaml',
    });

    expect(document.querySelector('a')).toBeInTheDocument();

    expect(createObjectMock).toHaveBeenCalledTimes(1);
    expect(createObjectMock).toHaveBeenCalledWith(blob);
  });
});

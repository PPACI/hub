import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';

import Sidebar from './Sidebar';

const defaultProps = {
  children: <span>Sidebar content</span>,
  header: 'title',
  label: 'test',
};

describe('Sidebar', () => {
  it('creates snapshot', () => {
    const { asFragment } = render(<Sidebar {...defaultProps} />);
    expect(asFragment()).toMatchSnapshot();
  });

  it('renders proper content', () => {
    render(<Sidebar {...defaultProps} />);
    expect(screen.getByRole('button', { name: /Open sidebar/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Close sidebar/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
    expect(screen.getByText(defaultProps.header)).toBeInTheDocument();
    expect(screen.getByText('Sidebar content')).toBeInTheDocument();
  });

  it('opens sidebar', () => {
    render(<Sidebar {...defaultProps} />);
    const sidebar = screen.getByRole('complementary', { name: 'Sidebar' });
    expect(sidebar).toBeInTheDocument();
    expect(sidebar).not.toHaveClass('active');
    const btn = screen.getByRole('button', { name: /Open sidebar/ });
    userEvent.click(btn);
    expect(sidebar).toHaveClass('active');
  });
});

--
-- PostgreSQL database dump
--

\restrict Xq3lZr0YbW2dV8mN1kTfH5sJ9cPeA6uG

-- Dumped from database version 17.6
-- Dumped by pg_dump version 17.6

SET statement_timeout = 0;
SET client_encoding = 'UTF8';

CREATE TABLE public.orders (
    id integer NOT NULL,
    total numeric(10,2)
);

COPY public.orders (id, total) FROM stdin;
1	19.99
2	5.00
\.

--
-- PostgreSQL database dump complete
--

\unrestrict Xq3lZr0YbW2dV8mN1kTfH5sJ9cPeA6uG

